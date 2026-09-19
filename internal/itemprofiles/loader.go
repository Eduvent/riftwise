package itemprofiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"lolengine/internal/catalog"
)

const maximumProfileFileBytes = 1 << 20
const maximumProfileFiles = 128

type Loader struct {
	Dir string
}

func (loader Loader) Resolve(activePatch string, knowledge catalog.Catalog) (Document, *Registry, Status, error) {
	active, ok := parsePatch(activePatch)
	if !ok {
		return Document{}, nil, Status{}, fmt.Errorf("invalid active catalog patch %q", activePatch)
	}
	if _, ok := parsePatch(knowledge.Patch); !ok {
		return Document{}, nil, Status{}, fmt.Errorf("invalid catalog patch %q", knowledge.Patch)
	}
	if activePatch != knowledge.Patch {
		return Document{}, nil, Status{}, fmt.Errorf("active patch %q does not match catalog patch %q", activePatch, knowledge.Patch)
	}
	directory := strings.TrimSpace(loader.Dir)
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return Document{}, nil, Status{}, fmt.Errorf("inspect generic profile directory: %w", err)
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 {
		return Document{}, nil, Status{}, errors.New("generic profile directory must not be a symbolic link")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return Document{}, nil, Status{}, fmt.Errorf("read generic profile directory: %w", err)
	}
	documents := make([]Document, 0, len(entries))
	patches := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		if len(documents) >= maximumProfileFiles {
			return Document{}, nil, Status{}, fmt.Errorf("generic profile directory exceeds %d JSON files", maximumProfileFiles)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return Document{}, nil, Status{}, fmt.Errorf("generic profile file %q must not be a symbolic link", entry.Name())
		}
		document, err := decodeDocument(filepath.Join(directory, entry.Name()))
		if err != nil {
			return Document{}, nil, Status{}, fmt.Errorf("load generic profiles %s: %w", entry.Name(), err)
		}
		if strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())) != document.Patch {
			return Document{}, nil, Status{}, fmt.Errorf("generic profile filename %q does not match patch %q", entry.Name(), document.Patch)
		}
		if _, exists := patches[document.Patch]; exists {
			return Document{}, nil, Status{}, fmt.Errorf("duplicate generic profile patch %q", document.Patch)
		}
		patches[document.Patch] = struct{}{}
		documents = append(documents, document)
	}
	if len(documents) == 0 {
		return Document{}, nil, Status{}, errors.New("generic profile directory contains no JSON documents")
	}
	sort.Slice(documents, func(left, right int) bool {
		leftPatch, _ := parsePatch(documents[left].Patch)
		rightPatch, _ := parsePatch(documents[right].Patch)
		return comparePatch(leftPatch, rightPatch) > 0
	})
	var selected *Document
	for index := range documents {
		if documents[index].Patch == activePatch {
			selected = &documents[index]
			break
		}
	}
	if selected == nil {
		for index := range documents {
			candidate, _ := parsePatch(documents[index].Patch)
			if candidate.major == active.major && candidate.minor == active.minor && comparePatch(candidate, active) <= 0 {
				selected = &documents[index]
				break
			}
		}
	}
	if selected == nil {
		return Document{}, nil, Status{}, fmt.Errorf("no compatible non-future generic profiles for catalog patch %s", activePatch)
	}
	registry, err := Compile(*selected, knowledge)
	if err != nil {
		return Document{}, nil, Status{}, fmt.Errorf("validate generic profiles %s: %w", selected.Patch, err)
	}
	fallback := selected.Patch != activePatch
	source := SourceExternalExact
	if fallback {
		source = SourceExternalPatchFallback
	}
	status := Status{Ready: true, Version: selected.RulesetVersion, Patch: selected.Patch, Source: source, Fallback: fallback}
	return cloneDocument(*selected), registry, status, nil
}

func decodeDocument(path string) (Document, error) {
	file, err := os.Open(path)
	if err != nil {
		return Document{}, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maximumProfileFileBytes+1))
	if err != nil {
		return Document{}, fmt.Errorf("read JSON: %w", err)
	}
	if len(payload) > maximumProfileFileBytes {
		return Document{}, fmt.Errorf("generic profile file exceeds %d bytes", maximumProfileFileBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Document{}, errors.New("trailing JSON value")
		}
		return Document{}, fmt.Errorf("trailing JSON: %w", err)
	}
	if err := validateDocumentShape(document); err != nil {
		return Document{}, err
	}
	return document, nil
}

type semanticPatch struct{ major, minor, patch int }

func parsePatch(value string) (semanticPatch, bool) {
	if value != strings.TrimSpace(value) {
		return semanticPatch{}, false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return semanticPatch{}, false
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		if part == "" {
			return semanticPatch{}, false
		}
		if len(part) > 1 && part[0] == '0' {
			return semanticPatch{}, false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return semanticPatch{}, false
			}
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return semanticPatch{}, false
		}
		numbers[index] = number
	}
	return semanticPatch{major: numbers[0], minor: numbers[1], patch: numbers[2]}, true
}

func comparePatch(left, right semanticPatch) int {
	leftParts := [...]int{left.major, left.minor, left.patch}
	rightParts := [...]int{right.major, right.minor, right.patch}
	for index := range leftParts {
		if leftParts[index] < rightParts[index] {
			return -1
		}
		if leftParts[index] > rightParts[index] {
			return 1
		}
	}
	return 0
}

// Provider is the generic profile boundary consumed by the itemization router.
type Provider interface {
	Resolve(string, Request, catalog.Catalog) Resolution
}

type Manager struct {
	loader        Loader
	mu            sync.Mutex
	resolvedFor   string
	current       *Registry
	currentStatus Status
	lastDocument  *Document
	revision      uint64
}

func NewManager(directory string) *Manager {
	return &Manager{loader: Loader{Dir: strings.TrimSpace(directory)}}
}

func (manager *Manager) Resolve(patch string, request Request, knowledge catalog.Catalog) Resolution {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.resolvedFor == patch && patch == knowledge.Patch {
		if manager.current == nil {
			return unavailableResolution(manager.currentStatus)
		}
		return applyStatus(manager.current.Resolve(request), manager.currentStatus)
	}
	return manager.reloadLocked(patch, request, knowledge)
}

func (manager *Manager) Reload(patch string, request Request, knowledge catalog.Catalog) Resolution {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.reloadLocked(patch, request, knowledge)
}

func (manager *Manager) Revision() uint64 {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.revision
}

func (manager *Manager) Status() Status {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.currentStatus
}

func (manager *Manager) reloadLocked(patch string, request Request, knowledge catalog.Catalog) Resolution {
	manager.revision++
	document, registry, status, err := manager.loader.Resolve(patch, knowledge)
	if err == nil {
		manager.current = registry
		manager.currentStatus = status
		manager.resolvedFor = patch
		clone := cloneDocument(document)
		manager.lastDocument = &clone
		return applyStatus(registry.Resolve(request), status)
	}
	if manager.lastDocument != nil && compatibleDocument(*manager.lastDocument, patch, knowledge) {
		if registry, compileErr := Compile(*manager.lastDocument, knowledge); compileErr == nil {
			status := Status{Ready: true, Version: manager.lastDocument.RulesetVersion, Patch: manager.lastDocument.Patch, Source: SourceExternalLastKnownGood, Fallback: true, LastError: err.Error()}
			manager.current = registry
			manager.currentStatus = status
			manager.resolvedFor = patch
			return applyStatus(registry.Resolve(request), status)
		}
	}
	status = Status{Ready: false, Source: SourceUnavailable, LastError: err.Error()}
	manager.current = nil
	manager.currentStatus = status
	manager.resolvedFor = patch
	return unavailableResolution(status)
}

func unavailableResolution(status Status) Resolution {
	return Resolution{Status: status, Warnings: []Warning{warning(WarningInsufficientContext)}}
}

func compatibleDocument(document Document, activePatch string, knowledge catalog.Catalog) bool {
	active, activeOK := parsePatch(activePatch)
	previous, previousOK := parsePatch(document.Patch)
	_, catalogOK := parsePatch(knowledge.Patch)
	return activePatch == knowledge.Patch && activeOK && previousOK && catalogOK &&
		active.major == previous.major && active.minor == previous.minor && comparePatch(previous, active) <= 0
}
