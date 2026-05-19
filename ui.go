package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type UIRenderer struct {
	state     *BridgeState
	firstRun  bool
	lineCount int
}

func NewUIRenderer(state *BridgeState) *UIRenderer {
	return &UIRenderer{
		state:     state,
		firstRun:  true,
		lineCount: 0,
	}
}

func (ur *UIRenderer) RenderStatus() {
	if ur.firstRun {
		// Full clear on first run
		fmt.Print("\033[H\033[2J")
		ur.firstRun = false
		ur.renderFull()
	} else {
		// Move cursor to top and render
		fmt.Print("\033[H")
		ur.renderFull()
	}
}

func (ur *UIRenderer) renderFull() {
	// Render header
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("33")).Render("OS2L Bridge Status")
	fmt.Println(header)
	fmt.Println(strings.Repeat("─", 80))

	// Status table
	ur.renderStatusTable()
	fmt.Println()

	// Decks table
	ur.renderDecksTable()

	// Clear rest of screen to avoid leftover characters
	fmt.Print("\033[J")
}

func (ur *UIRenderer) renderStatusTable() {
	fmt.Println("┌─────────────────────────┬─────────────┐")
	fmt.Println("│ Component               │ Status      │")
	fmt.Println("├─────────────────────────┼─────────────┤")

	vdjStatus := ur.getStatusString(ur.state.VDJConnected.Load())
	fmt.Printf("│ %-23s │ %-11s │\n", "VirtualDJ", vdjStatus)

	ssStatus := ur.getStatusString(ur.state.SSConnected.Load())
	fmt.Printf("│ %-23s │ %-11s │\n", "SoundSwitch", ssStatus)

	ssSubStatus := ""
	if ur.state.SSConnected.Load() {
		if ur.state.SSSubscribed.Load() {
			ssSubStatus = "Abonné"
		} else {
			ssSubStatus = "Non abonné"
		}
	} else {
		ssSubStatus = "N/A"
	}
	fmt.Printf("│ %-23s │ %-11s │\n", "SoundSwitch Sub.", ssSubStatus)

	fmt.Println("└─────────────────────────┴─────────────┘")
}

func (ur *UIRenderer) renderDecksTable() {
	deck1 := ur.state.GetDeck1()
	deck2 := ur.state.GetDeck2()

	fmt.Println("┌─────────────┬──────────────────────────────────┬──────────────────────────────────┐")
	fmt.Println("│ Property    │ Deck 1                           │ Deck 2                           │")
	fmt.Println("├─────────────┼──────────────────────────────────┼──────────────────────────────────┤")

	fmt.Printf("│ %-11s │ %-32s │ %-32s │\n", "File", truncate(getFilename(deck1.Filepath), 30), truncate(getFilename(deck2.Filepath), 30))
	fmt.Printf("│ %-11s │ %-32s │ %-32s │\n", "Time", timeFormat(deck1.TimeMs), timeFormat(deck2.TimeMs))
	fmt.Printf("│ %-11s │ %-32s │ %-32s │\n", "BPM", floatFormat(deck1.BPM, 1), floatFormat(deck2.BPM, 1))
	fmt.Printf("│ %-11s │ %-32s │ %-32s │\n", "Beat Pos", floatFormat(deck1.BeatPos, 2), floatFormat(deck2.BeatPos, 2))

	fmt.Println("└─────────────┴──────────────────────────────────┴──────────────────────────────────┘")
}

func (ur *UIRenderer) getStatusString(connected bool) string {
	if connected {
		return "Connecté"
	}
	return "Déconnecté"
}

func getFilename(filepath string) string {
	if filepath == "" {
		return "-"
	}
	parts := strings.Split(filepath, "\\")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return filepath
}

func truncate(s string, length int) string {
	if len(s) > length {
		return s[:length-3] + "..."
	}
	return s
}

func timeFormat(ms int64) string {
	totalSec := ms / 1000
	minutes := totalSec / 60
	seconds := totalSec % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func floatFormat(f float64, precision int) string {
	if f == 0 {
		return "-"
	}
	return fmt.Sprintf(fmt.Sprintf("%%.%df", precision), f)
}
