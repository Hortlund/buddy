package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"
	"buddy/internal/tailnet"
	"github.com/skip2/go-qrcode"
)

func setupTailnet(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	plan, err := tailnet.Configure(ctx, nil, "https://"+address, true)
	if err != nil {
		return err
	}
	fmt.Println(plan.Origin)
	return nil
}

func hostAccess(ctx context.Context, dir, address, origin string, phone bool) error {
	if origin == "" {
		origin = "https://" + address
	}
	if err := localconfig.ValidateServerURL(origin); err != nil {
		return err
	}
	client := api.NewClient("https://"+address, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := client.DesktopProtocol(ctx); err != nil {
		return fmt.Errorf("Buddy is not ready: %w", err)
	}
	if phone {
		link, expires, err := client.CreateMobilePairing(ctx, origin)
		if err != nil {
			return err
		}
		code, err := qrcode.New(link, qrcode.Medium)
		if err != nil {
			return err
		}
		fmt.Printf("Scan in Buddy for iPhone, or paste this link. Expires %s.\n%s\n%s\n", expires.Local().Format("15:04"), code.ToSmallString(false), link)
		return nil
	}
	profile, err := client.OwnerProfile(origin)
	if err != nil {
		return err
	}
	output := filepath.Join(dir, "owner.json")
	if err := localconfig.SaveConnection(output, profile, true); err != nil {
		return err
	}
	fmt.Printf("Buddy home: %s\nDesktop access file: %s\nOpen Connections → Connect to server in Buddy and choose this private file.\n", origin, output)
	return nil
}

func hostStatus(ctx context.Context, dir, address string) error {
	client := api.NewClient("https://"+address, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version, err := client.DesktopProtocol(ctx)
	if err != nil {
		return err
	}
	credential, err := client.CodexAuth(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Buddy server healthy · protocol %d\n", version)
	switch credential.State {
	case "missing":
		fmt.Fprintln(os.Stdout, "Codex executable is missing. Check the service's Codex path.")
	case "sign_in_required":
		fmt.Fprintln(os.Stdout, "Model sign-in needed. Run buddy-server login, or save an API key in desktop Connections.")
	case "unknown":
		fmt.Fprintln(os.Stdout, "Codex sign-in check timed out; try buddy-server status again.")
	default:
		fmt.Fprintf(os.Stdout, "Codex: %s (%s)\n", credential.State, credential.Source)
	}
	return nil
}
