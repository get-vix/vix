package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/kirby88/vix/internal/auth"
)

// runLogin implements `vix login [provider]`: it runs an OAuth login flow and
// stores the resulting credentials in the OS keychain. With no provider
// argument it prompts the user to choose one.
func runLogin(args []string) int {
	stdin := bufio.NewReader(os.Stdin)

	providerID := ""
	if len(args) > 0 {
		providerID = strings.TrimSpace(args[0])
	}
	if providerID == "" {
		chosen, ok := chooseProvider(stdin)
		if !ok {
			fmt.Fprintln(os.Stderr, "Login cancelled.")
			return 1
		}
		providerID = chosen
	}

	provider, ok := auth.GetProvider(providerID)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown provider %q. Available: %s\n", providerID, strings.Join(providerIDs(), ", "))
		return 1
	}

	cb := auth.LoginCallbacks{
		OnAuth: func(info auth.AuthInfo) {
			fmt.Println()
			fmt.Println("Open the following URL in your browser to authenticate:")
			fmt.Println()
			fmt.Println("  " + info.URL)
			fmt.Println()
			if info.Instructions != "" {
				fmt.Println(info.Instructions)
			}
			if err := openBrowser(info.URL); err == nil {
				fmt.Println("(Attempted to open your browser automatically.)")
			}
			fmt.Println()
		},
		OnDeviceCode: func(info auth.DeviceCodeInfo) {
			fmt.Println()
			fmt.Printf("Visit %s and enter the code:\n\n", info.VerificationURI)
			fmt.Printf("  %s\n\n", info.UserCode)
			if err := openBrowser(info.VerificationURI); err == nil {
				fmt.Println("(Attempted to open your browser automatically.)")
			}
			fmt.Println("Waiting for authorization...")
		},
		OnProgress: func(msg string) {
			fmt.Println(msg)
		},
		OnPrompt: func(p auth.Prompt) (string, error) {
			label := p.Message
			if p.Placeholder != "" {
				label += " [" + p.Placeholder + "]"
			}
			fmt.Print(label + ": ")
			return readLine(stdin)
		},
		OnSelect: func(p auth.SelectPrompt) (string, error) {
			return selectOption(stdin, p), nil
		},
	}

	fmt.Printf("Logging in to %s...\n", provider.Name())
	if err := auth.DefaultStorage().Login(context.Background(), providerID, cb); err != nil {
		fmt.Fprintf(os.Stderr, "\nLogin failed: %v\n", err)
		if p := auth.AuthLogPath(); p != "" {
			fmt.Fprintf(os.Stderr, "Detailed logs: %s\n", p)
		}
		return 1
	}

	fmt.Printf("\n✓ Logged in to %s.\n", provider.Name())
	fmt.Printf("  Credentials stored in: %s\n", auth.DefaultStorageLocation())
	return 0
}

// runLogout implements `vix logout <provider>`: it removes stored OAuth
// credentials for a provider.
func runLogout(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: vix logout <provider>\nAvailable: %s\n", strings.Join(providerIDs(), ", "))
		return 1
	}
	providerID := strings.TrimSpace(args[0])
	provider, ok := auth.GetProvider(providerID)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown provider %q. Available: %s\n", providerID, strings.Join(providerIDs(), ", "))
		return 1
	}
	if !auth.DefaultStorage().HasLogin(providerID) {
		fmt.Printf("Not logged in to %s.\n", provider.Name())
		return 0
	}
	if err := auth.DefaultStorage().Logout(providerID); err != nil {
		fmt.Fprintf(os.Stderr, "Logout failed: %v\n", err)
		return 1
	}
	fmt.Printf("✓ Logged out of %s.\n", provider.Name())
	return 0
}

func providerIDs() []string {
	providers := auth.GetProviders()
	ids := make([]string, 0, len(providers))
	for _, p := range providers {
		ids = append(ids, p.ID())
	}
	return ids
}

// chooseProvider prompts the user to pick a provider from the registry.
func chooseProvider(stdin *bufio.Reader) (string, bool) {
	providers := auth.GetProviders()
	fmt.Println("Select a provider to log in to:")
	for i, p := range providers {
		fmt.Printf("  %d) %s (%s)\n", i+1, p.Name(), p.ID())
	}
	fmt.Print("Enter a number: ")
	line, err := readLine(stdin)
	if err != nil {
		return "", false
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(providers) {
		return "", false
	}
	return providers[n-1].ID(), true
}

// selectOption renders a SelectPrompt and returns the chosen option id, or ""
// on invalid input / cancellation.
func selectOption(stdin *bufio.Reader, p auth.SelectPrompt) string {
	fmt.Println()
	fmt.Println(p.Message)
	for i, opt := range p.Options {
		fmt.Printf("  %d) %s\n", i+1, opt.Label)
	}
	fmt.Print("Enter a number: ")
	line, err := readLine(stdin)
	if err != nil {
		return ""
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(p.Options) {
		return ""
	}
	return p.Options[n-1].ID
}

// readLine reads a single line from r, trimming the trailing newline. EOF with
// buffered data returns that data; EOF with no data returns an error.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

// openBrowser best-effort opens url in the user's default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
