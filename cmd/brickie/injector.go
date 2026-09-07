package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/hatchet-dev/brickie/internal/injector"
)

func injectorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "injector",
		Short: "Credential injector: a tokenizer-style proxy that swaps sealed secrets at egress",
	}
	cmd.AddCommand(injectorServeCmd(), injectorKeygenCmd(), injectorSealCmd())
	return cmd
}

func injectorServeCmd() *cobra.Command {
	var listen string
	var hatchetListen string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the injector proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			publicKey := os.Getenv("BRICKIE_INJECTOR_PUBLIC_KEY")
			privateKey := os.Getenv("BRICKIE_INJECTOR_PRIVATE_KEY")
			if publicKey == "" || privateKey == "" {
				return errors.New("BRICKIE_INJECTOR_PUBLIC_KEY and BRICKIE_INJECTOR_PRIVATE_KEY are required (run `brickie injector keygen`)")
			}
			if token := os.Getenv("HATCHET_CLIENT_TOKEN"); token != "" && hatchetListen != "" {
				proxy, err := injector.NewHatchetProxyFromToken(token, os.Getenv("HATCHET_CLIENT_TLS_STRATEGY") == "none")
				if err != nil {
					return err
				}
				go func() {
					fmt.Fprintf(os.Stderr, "brickie: hatchet proxy listening on %s (grpc upstream %s)\n", hatchetListen, proxy.GRPCUpstream)
					if err := proxy.Serve(hatchetListen); err != nil {
						fmt.Fprintf(os.Stderr, "brickie: hatchet proxy: %v\n", err)
					}
				}()
			}
			fmt.Fprintf(os.Stderr, "brickie: credential injector listening on %s\n", listen)
			return http.ListenAndServe(listen, &injector.Server{PublicKey: publicKey, PrivateKey: privateKey})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", envOr("BRICKIE_INJECTOR_LISTEN", ":3128"), "listen address")
	cmd.Flags().StringVar(&hatchetListen, "hatchet-listen", envOr("BRICKIE_INJECTOR_HATCHET_LISTEN", "127.0.0.1:7077"), "hatchet proxy listen address (empty to disable)")
	return cmd
}

func injectorKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Generate an injector keypair",
		RunE: func(cmd *cobra.Command, args []string) error {
			pub, priv, err := injector.GenerateKeypair()
			if err != nil {
				return err
			}
			fmt.Printf("BRICKIE_INJECTOR_PUBLIC_KEY=%s\nBRICKIE_INJECTOR_PRIVATE_KEY=%s\n", pub, priv)
			return nil
		},
	}
}

func injectorSealCmd() *cobra.Command {
	var (
		publicKey string
		token     string
		tokenEnv  string
		hosts     []string
		authToken string
	)
	cmd := &cobra.Command{
		Use:   "seal",
		Short: "Seal a credential for the injector",
		RunE: func(cmd *cobra.Command, args []string) error {
			if publicKey == "" {
				return errors.New("--public-key or BRICKIE_INJECTOR_PUBLIC_KEY is required")
			}
			value := token
			if value == "" && tokenEnv != "" {
				value = os.Getenv(tokenEnv)
			}
			if value == "" {
				return errors.New("--token or --token-env is required")
			}
			if len(hosts) == 0 {
				return errors.New("at least one --host is required")
			}
			secret := injector.Secret{Token: value, AllowedHosts: hosts}
			if authToken != "" {
				secret.AuthDigest = injector.AuthDigest(authToken)
			}
			sealed, err := injector.Seal(publicKey, secret)
			if err != nil {
				return err
			}
			fmt.Println(sealed)
			return nil
		},
	}
	cmd.Flags().StringVar(&publicKey, "public-key", os.Getenv("BRICKIE_INJECTOR_PUBLIC_KEY"), "injector public key")
	cmd.Flags().StringVar(&token, "token", "", "credential value to seal")
	cmd.Flags().StringVar(&tokenEnv, "token-env", "", "read the credential value from this env var")
	cmd.Flags().StringArrayVar(&hosts, "host", nil, "allowed upstream host (repeatable, *.example.com supported)")
	cmd.Flags().StringVar(&authToken, "auth", "", "client auth token the sandbox must present to use this secret")
	return cmd
}
