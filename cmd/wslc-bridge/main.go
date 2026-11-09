package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"wslc-bridge/internal/app"
	"wslc-bridge/internal/buildinfo"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var exitErr *app.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "version":
		return printVersion(args)
	case "doctor":
		return printDoctor(args)
	case "serve":
		return serve(args)
	default:
		return &app.ExitError{Code: 2, Message: fmt.Sprintf("unknown command %q (expected serve, doctor, or version)", command)}
	}
}

func printVersion(args []string) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print machine-readable version information")
	if err := flags.Parse(args); err != nil {
		return &app.ExitError{Code: 2, Message: err.Error()}
	}
	info := buildinfo.Info()
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(info)
	}
	fmt.Printf("wslc-bridge %s (%s/%s, Go %s)\n", info.Version, info.OS, info.Arch, info.GoVersion)
	return nil
}

func printDoctor(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print machine-readable diagnostics")
	if err := flags.Parse(args); err != nil {
		return &app.ExitError{Code: 2, Message: err.Error()}
	}
	result := app.Doctor(context.Background())
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
	} else {
		fmt.Printf("Platform: %s/%s\n", result.OS, result.Arch)
		fmt.Printf("WSLC: %s\n", valueOrUnavailable(result.WSLCVersion))
		fmt.Printf("WSL: %s\n", valueOrUnavailable(result.WSLVersion))
		fmt.Printf("Guest Docker API: %s\n", valueOrUnavailable(result.GuestAPIVersion))
		if result.Error != "" {
			fmt.Printf("Status: %s\n", result.Error)
		} else {
			fmt.Println("Status: ready")
		}
	}
	if result.Error != "" {
		return &app.ExitError{Code: result.ExitCode, Message: result.Error}
	}
	return nil
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	pipeName := flags.String("pipe", app.DefaultPipeName, "Windows named pipe path")
	shutdownOnStdinEOF := flags.Bool("shutdown-on-stdin-eof", false, "gracefully stop when stdin closes (for extension child-process lifecycle)")
	if err := flags.Parse(args); err != nil {
		return &app.ExitError{Code: 2, Message: err.Error()}
	}
	if flags.NArg() != 0 {
		return &app.ExitError{Code: 2, Message: "serve does not accept positional arguments"}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *shutdownOnStdinEOF {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}
	return app.Serve(ctx, *pipeName)
}

func valueOrUnavailable(value string) string {
	if value == "" {
		return "unavailable"
	}
	return value
}
