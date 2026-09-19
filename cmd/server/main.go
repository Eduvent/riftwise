package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lolengine/internal/api"
	"lolengine/internal/catalog"
	"lolengine/internal/config"
	"lolengine/internal/ddragon"
	"lolengine/internal/domain"
	"lolengine/internal/history"
	"lolengine/internal/itemization"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/liveclient"
	"lolengine/internal/matchsync"
	"lolengine/internal/personalization"
	"lolengine/internal/pregame"
	"lolengine/internal/recommendation"
	"lolengine/internal/riotapi"
	"lolengine/internal/state"
	"lolengine/internal/statistics"
	"lolengine/internal/storage"
	"lolengine/internal/tracker"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("lol-engine: %v", err)
	}
}

func run() error {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	processContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	liveClient, err := liveclient.New(cfg.LiveClientURL)
	if err != nil {
		return fmt.Errorf("configure live client: %w", err)
	}
	database, err := storage.Open(processContext, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open local storage: %w", err)
	}
	defer database.Close()

	store := state.NewStore(domain.Snapshot{
		Status:     domain.StatusWaitingForGame,
		Connection: domain.ConnectionWaiting,
		UpdatedAt:  time.Now().UTC(),
	})
	gameTracker, err := tracker.New(liveClient, store, tracker.Config{
		PollInterval: cfg.PollInterval,
		StaleAfter:   cfg.StaleAfter,
		EndAfter:     cfg.EndAfter,
		Persistence:  database,
	})
	if err != nil {
		return fmt.Errorf("configure tracker: %w", err)
	}
	dataDragon, err := ddragon.New(cfg.DDragonBaseURL, nil, ddragon.Config{Timeout: cfg.DDragonTimeout})
	if err != nil {
		return fmt.Errorf("configure Data Dragon: %w", err)
	}
	catalogs, err := catalog.NewManager(dataDragon, database, cfg.DDragonLocale)
	if err != nil {
		return fmt.Errorf("configure catalog manager: %w", err)
	}
	if err := catalogs.Initialize(processContext, cfg.DDragonSync); err != nil {
		log.Printf("lol-engine catalog startup warning: %v", err)
	}
	rules := itemrules.NewManager(cfg.RulesDir)
	profiles := itemprofiles.NewManager(cfg.GenericRulesDir)
	personalizationService, err := personalization.NewService(database, catalogs, personalization.Config{})
	if err != nil {
		return fmt.Errorf("configure personalization: %w", err)
	}
	manualPlanner, err := pregame.NewService(
		catalogs,
		itemrules.NewManager(cfg.RulesDir),
		itemprofiles.NewManager(cfg.GenericRulesDir),
		personalizationService,
	)
	if err != nil {
		return fmt.Errorf("configure manual pregame planner: %w", err)
	}
	recommendations, err := recommendation.NewCoordinator(
		store,
		catalogs,
		itemization.Engine{Rules: rules, Profiles: profiles, GenericEnabled: cfg.GenericEnabled, Strategy: cfg.Strategy, Personalization: personalizationService},
		database,
		recommendation.Config{},
	)
	if err != nil {
		return fmt.Errorf("configure recommendation coordinator: %w", err)
	}
	personalizationService.SetFeedbackEligibilityProvider(recommendations)
	matchHistory, err := history.NewService(database, history.Config{
		Fence:   recommendations,
		OnError: func(err error) { log.Printf("lol-engine history maintenance warning: %v", err) },
	})
	if err != nil {
		return fmt.Errorf("configure match history: %w", err)
	}
	var riotClient riotapi.Service
	switch cfg.RiotMode {
	case riotapi.ModeDirect:
		riotClient, err = riotapi.NewDirect(cfg.RiotAPIKey, nil, riotapi.ClientConfig{Timeout: cfg.RiotTimeout})
	case riotapi.ModeBackend:
		riotClient, err = riotapi.NewBackend(cfg.RiotBackendURL, nil, riotapi.ClientConfig{Timeout: cfg.RiotTimeout})
	}
	if err != nil {
		return fmt.Errorf("configure optional Riot API adapter: %w", err)
	}
	riotSync, err := matchsync.New(riotClient, database, matchsync.Config{
		Mode: cfg.RiotMode, Region: cfg.RiotRegion, PageSize: cfg.RiotPageSize,
		MaxMatches: cfg.RiotMaxMatches, Concurrency: cfg.RiotConcurrency, AutoSync: cfg.RiotAutoSync,
	})
	if err != nil {
		return fmt.Errorf("configure optional Riot match synchronization: %w", err)
	}
	historicalStatistics := statistics.NewService(database, 10)

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddress, err)
	}
	defer listener.Close()

	httpServer := &http.Server{
		Handler: api.New(store, api.Config{
			ApplicationContext:     processContext,
			Recommendations:        recommendations,
			MatchHistory:           database,
			History:                matchHistory,
			Pregame:                manualPlanner,
			GenericProfilesEnabled: cfg.GenericEnabled,
			RiotSync:               riotSync,
			Statistics:             historicalStatistics,
			Personalization:        personalizationService,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		BaseContext: func(net.Listener) context.Context {
			return processContext
		},
	}

	trackerContext, stopTracker := context.WithCancel(processContext)
	defer stopTracker()
	go gameTracker.Run(trackerContext)
	go recommendations.Run(trackerContext)
	go matchHistory.Run(trackerContext)
	if cfg.RiotAutoSync && cfg.RiotMode != riotapi.ModeDisabled {
		go func() {
			status, statusErr := riotSync.Status(trackerContext)
			if statusErr != nil {
				log.Printf("lol-engine Riot auto-sync status warning: %v", statusErr)
				return
			}
			for _, account := range status.Accounts {
				if _, syncErr := riotSync.Sync(trackerContext, account.ID, matchsync.Filter{}); syncErr != nil && trackerContext.Err() == nil {
					// Errors are safe here because riotapi.Error never contains a response body or key.
					log.Printf("lol-engine Riot auto-sync warning: %v", syncErr)
				}
			}
		}()
	}

	serveErrors := make(chan error, 1)
	go func() {
		log.Printf("lol-engine listening on http://%s", listener.Addr())
		serveErrors <- httpServer.Serve(listener)
	}()

	select {
	case <-processContext.Done():
		log.Printf("lol-engine shutting down")
	case serveErr := <-serveErrors:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", serveErr)
		}
		return nil
	}

	stopTracker()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	return nil
}
