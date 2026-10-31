package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

func terminalFixture() {
	previous, err := term.MakeRaw(0)
	if err != nil {
		panic(err)
	}
	defer term.Restore(0, previous)
	fmt.Print("INFERENCE_READY\r\n")
	var input [1]byte
	for {
		if _, err := os.Stdin.Read(input[:]); err != nil {
			return
		}
		switch input[0] {
		case 'b':
			fmt.Print("\x1b]2;Action Required\x07")
		case 'w':
			fmt.Print("\x1b]2;⠋ Working\x07")
		case 'd':
			fmt.Print("\x1b]2;Codex\x07\x1b[2J\x1b[H──── Worked for 3s ────\r\n")
		case 'p':
			fmt.Print("\x1b]9;4;1;37\x07")
		case 'r':
			fmt.Print("\x1b]9;4;0\x07")
		case 'u':
			fmt.Print("UNFINISHED_BEFORE\r\n\x1b]2;pending title")
			continue
		case 'f':
			fmt.Print("\x07FINISHED_AFTER\r\n")
		case 'g':
			fmt.Print("FORCED_AFTER\r\n")
		case 'x':
			os.Exit(7)
		}
		fmt.Printf("INFERENCE_SENT:%c\r\n", input[0])
	}
}
