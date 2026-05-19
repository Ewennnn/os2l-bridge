package main

import (
	"encoding/json"
	"regexp"
	"sync"
	"sync/atomic"
)

// DeckInfo stores information for a single deck
type DeckInfo struct {
	Filepath  string
	TimeMs    int64
	BeatPos   float64
	BPM       float64
	FirstBeat float64
}

// BridgeState tracks the state of the bridge and connected devices
type BridgeState struct {
	VDJConnected     atomic.Bool
	SSConnected      atomic.Bool
	SSSubscribed     atomic.Bool
	SSSubscribeCount atomic.Int32

	Deck1 DeckInfo
	Deck2 DeckInfo

	mu sync.RWMutex
}

// Event represents an OS2L message event for the UI
type Event struct {
	Direction string      // "VDJ -> SS" or "SS -> VDJ"
	Trigger   string      // The trigger/event name
	Value     interface{} // The value
}

// NewBridgeState creates a new bridge state
func NewBridgeState() *BridgeState {
	return &BridgeState{}
}

// UpdateFromOS2LMessage processes an OS2L message and updates state
func (bs *BridgeState) UpdateFromOS2LMessage(direction string, msgJSON string) *Event {
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(msgJSON), &msg); err != nil {
		return nil
	}

	evt, ok := msg["evt"].(string)
	if !ok {
		return nil
	}

	// Track subscription state for SoundSwitch
	if direction == "SS -> VDJ" && evt == "subscribe" {
		bs.SSSubscribeCount.Store(0)
		bs.SSSubscribed.Store(false)
		return nil
	}

	// Extract trigger and value
	trigger, _ := msg["trigger"].(string)
	value, _ := msg["value"]

	// Count subscription confirmations from VirtualDJ
	if direction == "VDJ -> SS" && evt == "subscribed" {
		// Only count deck-related subscriptions (deck 1, 2, 3, 4)
		if trigger != "" && isRelevantDeckTrigger(trigger) {
			count := bs.SSSubscribeCount.Add(1)
			// Mark as subscribed when we receive 4 subscribed confirmations
			if count >= 4 {
				bs.SSSubscribed.Store(true)
			}
		}
	}

	// Update deck info for any message with deck trigger and value from VDJ
	if direction == "VDJ -> SS" && trigger != "" && value != nil && isRelevantDeckTrigger(trigger) {
		bs.updateDeckInfo(trigger, value)
	}

	if trigger == "" {
		return nil
	}

	return &Event{
		Direction: direction,
		Trigger:   trigger,
		Value:     value,
	}
}

func (bs *BridgeState) updateDeckInfo(trigger string, value interface{}) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	// Deck 1
	if deckNum := extractDeckNumber(trigger); deckNum == 1 {
		if trigger == "deck 1 get_filepath" {
			if v, ok := value.(string); ok {
				bs.Deck1.Filepath = v
			}
		} else if trigger == "deck 1 get_time elapsed absolute" {
			if v, ok := value.(float64); ok {
				bs.Deck1.TimeMs = int64(v)
			}
		} else if trigger == "deck 1 get_beatpos" {
			if v, ok := value.(float64); ok {
				bs.Deck1.BeatPos = v
			}
		} else if trigger == "deck 1 get_bpm" {
			if v, ok := value.(float64); ok {
				bs.Deck1.BPM = v
			}
		} else if trigger == "deck 1 get_firstbeat" {
			if v, ok := value.(float64); ok {
				bs.Deck1.FirstBeat = v
			}
		}
	}

	// Deck 2
	if deckNum := extractDeckNumber(trigger); deckNum == 2 {
		if trigger == "deck 2 get_filepath" {
			if v, ok := value.(string); ok {
				bs.Deck2.Filepath = v
			}
		} else if trigger == "deck 2 get_time elapsed absolute" {
			if v, ok := value.(float64); ok {
				bs.Deck2.TimeMs = int64(v)
			}
		} else if trigger == "deck 2 get_beatpos" {
			if v, ok := value.(float64); ok {
				bs.Deck2.BeatPos = v
			}
		} else if trigger == "deck 2 get_bpm" {
			if v, ok := value.(float64); ok {
				bs.Deck2.BPM = v
			}
		} else if trigger == "deck 2 get_firstbeat" {
			if v, ok := value.(float64); ok {
				bs.Deck2.FirstBeat = v
			}
		}
	}
}

// GetDeck1 returns a copy of deck 1 info
func (bs *BridgeState) GetDeck1() DeckInfo {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return bs.Deck1
}

// GetDeck2 returns a copy of deck 2 info
func (bs *BridgeState) GetDeck2() DeckInfo {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return bs.Deck2
}

// Helper functions

func extractDeckNumber(trigger string) int {
	re := regexp.MustCompile(`deck (\d)`)
	matches := re.FindStringSubmatch(trigger)
	if len(matches) == 2 {
		if matches[1] == "1" {
			return 1
		} else if matches[1] == "2" {
			return 2
		}
	}
	return 0
}

func isRelevantDeckTrigger(trigger string) bool {
	relevantPatterns := []string{
		"deck 1",
		"deck 2",
		"deck 3",
		"deck 4",
	}
	for _, pattern := range relevantPatterns {
		if matched, _ := regexp.MatchString(pattern, trigger); matched {
			return true
		}
	}
	return false
}
