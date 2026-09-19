package itemrules

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

const maximumRuleFileBytes = 1 << 20
const maximumRuleFiles = 128

// Loader reads strict JSON rules from one champion/version directory.
type Loader struct {
	Dir string
}

// Resolve selects an exact patch, then the newest non-future patch in the same major.minor.
func (loader Loader) Resolve(activePatch string, knowledge catalog.Catalog) (RuleSet, Status, error) {
	active, ok := parsePatch(activePatch)
	if !ok {
		return RuleSet{}, Status{}, fmt.Errorf("invalid active catalog patch %q", activePatch)
	}
	if _, ok := parsePatch(knowledge.Patch); !ok {
		return RuleSet{}, Status{}, fmt.Errorf("invalid catalog patch %q", knowledge.Patch)
	}
	if activePatch != knowledge.Patch {
		return RuleSet{}, Status{}, fmt.Errorf("active patch %q does not match catalog patch %q", activePatch, knowledge.Patch)
	}
	entries, err := os.ReadDir(strings.TrimSpace(loader.Dir))
	if err != nil {
		return RuleSet{}, Status{}, fmt.Errorf("read rules directory: %w", err)
	}
	sets := make([]RuleSet, 0, len(entries))
	patches := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		if len(sets) >= maximumRuleFiles {
			return RuleSet{}, Status{}, fmt.Errorf("rules directory exceeds %d JSON files", maximumRuleFiles)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return RuleSet{}, Status{}, fmt.Errorf("rules file %q must not be a symbolic link", entry.Name())
		}
		path := filepath.Join(loader.Dir, entry.Name())
		value, err := decodeRuleFile(path)
		if err != nil {
			return RuleSet{}, Status{}, fmt.Errorf("load rules %s: %w", entry.Name(), err)
		}
		if strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())) != value.Patch {
			return RuleSet{}, Status{}, fmt.Errorf("rules filename %q does not match patch %q", entry.Name(), value.Patch)
		}
		if _, exists := patches[value.Patch]; exists {
			return RuleSet{}, Status{}, fmt.Errorf("duplicate rules patch %q", value.Patch)
		}
		patches[value.Patch] = struct{}{}
		sets = append(sets, value)
	}
	if len(sets) == 0 {
		return RuleSet{}, Status{}, errors.New("rules directory contains no JSON rulesets")
	}
	sort.Slice(sets, func(left, right int) bool {
		leftPatch, _ := parsePatch(sets[left].Patch)
		rightPatch, _ := parsePatch(sets[right].Patch)
		return comparePatch(leftPatch, rightPatch) > 0
	})
	var selected *RuleSet
	for index := range sets {
		if sets[index].Patch == activePatch {
			selected = &sets[index]
			break
		}
	}
	if selected == nil {
		for index := range sets {
			candidate, _ := parsePatch(sets[index].Patch)
			if candidate.major == active.major && candidate.minor == active.minor && comparePatch(candidate, active) <= 0 {
				selected = &sets[index]
				break
			}
		}
	}
	if selected == nil {
		return RuleSet{}, Status{}, fmt.Errorf("no compatible non-future rules for catalog patch %s", activePatch)
	}
	if err := selected.Validate(knowledge); err != nil {
		return RuleSet{}, Status{}, fmt.Errorf("validate rules %s: %w", selected.Patch, err)
	}
	fallback := selected.Patch != activePatch
	source := SourceExternalExact
	if fallback {
		source = SourceExternalFallback
	}
	value := cloneRuleSet(*selected)
	return value, Status{Ready: true, Version: value.RulesetVersion, Patch: value.Patch, Source: source, Fallback: fallback}, nil
}

func decodeRuleFile(path string) (RuleSet, error) {
	file, err := os.Open(path)
	if err != nil {
		return RuleSet{}, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maximumRuleFileBytes+1))
	if err != nil {
		return RuleSet{}, fmt.Errorf("read JSON: %w", err)
	}
	if len(payload) > maximumRuleFileBytes {
		return RuleSet{}, fmt.Errorf("rules file exceeds %d bytes", maximumRuleFileBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value RuleSet
	if err := decoder.Decode(&value); err != nil {
		return RuleSet{}, fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return RuleSet{}, errors.New("trailing JSON value")
		}
		return RuleSet{}, fmt.Errorf("trailing JSON: %w", err)
	}
	if err := value.validateStructure(); err != nil {
		return RuleSet{}, err
	}
	return value, nil
}

type semanticPatch struct {
	major int
	minor int
	patch int
}

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

// Manager caches the active immutable rules and retains the last known good set on reload failure.
type Manager struct {
	loader Loader

	mu            sync.Mutex
	resolvedFor   string
	current       *Resolution
	lastKnownGood *Resolution
	revision      uint64
}

func NewManager(directory string) *Manager {
	return &Manager{loader: Loader{Dir: strings.TrimSpace(directory)}}
}

func (manager *Manager) Resolve(patch string, knowledge catalog.Catalog) Resolution {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.current != nil && manager.resolvedFor == patch && patch == knowledge.Patch {
		return cloneResolution(*manager.current)
	}
	return manager.reloadLocked(patch, knowledge)
}

// Reload forces a disk read and keeps a compatible last-known-good ruleset on failure.
func (manager *Manager) Reload(patch string, knowledge catalog.Catalog) Resolution {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.reloadLocked(patch, knowledge)
}

// Revision advances whenever a real resolve/reload attempt changes cached rule state.
func (manager *Manager) Revision() uint64 {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.revision
}

func (manager *Manager) reloadLocked(patch string, knowledge catalog.Catalog) Resolution {
	manager.revision++
	rules, status, err := manager.loader.Resolve(patch, knowledge)
	if err == nil {
		resolution := Resolution{Rules: rules, Status: status}
		manager.current = pointerResolution(cloneResolution(resolution))
		manager.lastKnownGood = pointerResolution(cloneResolution(resolution))
		manager.resolvedFor = patch
		return cloneResolution(resolution)
	}
	if manager.lastKnownGood != nil && compatibleLastKnownGood(manager.lastKnownGood.Rules, patch, knowledge) {
		resolution := cloneResolution(*manager.lastKnownGood)
		resolution.Status.Source = SourceExternalLastKnownGood
		resolution.Status.Fallback = true
		resolution.Status.LastError = err.Error()
		manager.current = pointerResolution(cloneResolution(resolution))
		manager.resolvedFor = patch
		return resolution
	}
	resolution := Resolution{
		Legacy: true,
		Status: Status{
			Ready: true, Version: "legacy-v1", Source: SourceLegacyBuiltin,
			Fallback: true, Legacy: true, LastError: err.Error(),
		},
	}
	manager.current = pointerResolution(resolution)
	manager.resolvedFor = patch
	return resolution
}

func compatibleLastKnownGood(rules RuleSet, activePatch string, knowledge catalog.Catalog) bool {
	active, activeOK := parsePatch(activePatch)
	previous, previousOK := parsePatch(rules.Patch)
	_, catalogOK := parsePatch(knowledge.Patch)
	return activePatch == knowledge.Patch && activeOK && previousOK && catalogOK &&
		active.major == previous.major && active.minor == previous.minor &&
		comparePatch(previous, active) <= 0 && rules.Validate(knowledge) == nil
}

func pointerResolution(value Resolution) *Resolution {
	return &value
}

func cloneResolution(source Resolution) Resolution {
	clone := source
	clone.Rules = cloneRuleSet(source.Rules)
	return clone
}

func cloneRuleSet(source RuleSet) RuleSet {
	payload, err := json.Marshal(source)
	if err != nil {
		return source
	}
	var clone RuleSet
	if err := json.Unmarshal(payload, &clone); err != nil {
		return source
	}
	return clone
}
