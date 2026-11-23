// Command tools provides small helper commands used by run.sh. Moving the
// conformance harness's JSON/config helpers here removes its Python dependency
// and puts their data contracts under the Go compiler's checks.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/alecthomas/kong"
)

type cli struct {
	Spectest spectestCmd `cmd:"" help:"Helpers for the external JMAP conformance suite."`
}

type spectestCmd struct {
	LoginBody     loginBodyCmd     `cmd:"" name:"login-body" help:"Print the JSON body for POST /api/auth/login."`
	LoginToken    loginTokenCmd    `cmd:"" name:"login-token" help:"Read a login response from stdin and print its access token."`
	UserBody      userBodyCmd      `cmd:"" name:"user-body" help:"Print the JSON body for POST /api/user/."`
	WriteConfig   writeConfigCmd   `cmd:"" name:"write-config" help:"Write the conformance suite config file."`
	ReportSummary reportSummaryCmd `cmd:"" name:"report-summary" help:"Print the pass/fail summary from a suite report."`
}

type loginBodyCmd struct {
	Username string `arg:"" help:"Login username."`
	Password string `arg:"" help:"Login password."`
}

type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c *loginBodyCmd) Run() error {
	return json.NewEncoder(os.Stdout).Encode(loginBody{
		Username: c.Username,
		Password: c.Password,
	})
}

type loginTokenCmd struct{}

func (c *loginTokenCmd) Run() error {
	var response struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&response); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	if response.AccessToken == "" {
		return errors.New("login response has no accessToken")
	}
	fmt.Println(response.AccessToken)
	return nil
}

type userBodyCmd struct {
	Email    string `arg:"" help:"User email address."`
	Password string `arg:"" help:"User password."`
	Manager  string `arg:"" help:"Whether the user is a user and system manager (true or false)."`
}

type createUserBody struct {
	Email         string `json:"email"`
	Password      string `json:"password"`
	Enabled       bool   `json:"enabled"`
	UserManager   bool   `json:"userManager"`
	SystemManager bool   `json:"systemManager"`
}

func (c *userBodyCmd) Run() error {
	manager, err := strconv.ParseBool(c.Manager)
	if err != nil {
		return fmt.Errorf("invalid manager value %q: %w", c.Manager, err)
	}
	return json.NewEncoder(os.Stdout).Encode(createUserBody{
		Email:         c.Email,
		Password:      c.Password,
		Enabled:       true,
		UserManager:   manager,
		SystemManager: manager,
	})
}

type writeConfigCmd struct {
	Path              string `arg:"" help:"Destination config file."`
	BaseURL           string `arg:"" help:"Base URL of the local server."`
	Username          string `arg:"" help:"Primary suite user."`
	Token             string `arg:"" help:"Primary bearer token."`
	SecondaryUsername string `arg:"" help:"Secondary suite user."`
	SecondaryToken    string `arg:"" help:"Secondary bearer token."`
}

type suiteUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type suiteUsers struct {
	Primary   suiteUser `json:"primary"`
	Secondary suiteUser `json:"secondary"`
}

type suiteConfig struct {
	SessionURL string     `json:"sessionUrl"`
	ServerInfo string     `json:"serverInfo"`
	Users      suiteUsers `json:"users"`
	AuthMethod string     `json:"authMethod"`
	Timeout    int        `json:"timeout"`
	Verbose    bool       `json:"verbose"`
}

func (c *writeConfigCmd) Run() error {
	config := suiteConfig{
		SessionURL: c.BaseURL + "/.well-known/jmap",
		ServerInfo: "Emissary local conformance",
		Users: suiteUsers{
			Primary:   suiteUser{Username: c.Username, Password: c.Token},
			Secondary: suiteUser{Username: c.SecondaryUsername, Password: c.SecondaryToken},
		},
		AuthMethod: "bearer",
		Timeout:    30000,
		Verbose:    false,
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode suite config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return fmt.Errorf("create suite config directory: %w", err)
	}
	// The config carries bearer tokens; keep it private to this user.
	if err := os.WriteFile(c.Path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write suite config: %w", err)
	}
	return nil
}

type reportSummaryCmd struct {
	Path string `arg:"" help:"Suite report JSON file."`
}

type conformanceSummary struct {
	Passed         int `json:"passed"`
	Failed         int `json:"failed"`
	Skipped        int `json:"skipped"`
	RequiredPassed int `json:"requiredPassed"`
	RequiredFailed int `json:"requiredFailed"`
}

type conformanceReport struct {
	Summary *conformanceSummary `json:"summary"`
}

func (c *reportSummaryCmd) Run() error {
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return fmt.Errorf("read suite report: %w", err)
	}
	var report conformanceReport
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("decode suite report: %w", err)
	}
	if report.Summary == nil {
		return errors.New("suite report has no summary")
	}
	s := report.Summary
	fmt.Printf(
		"JMAP conformance: %d passed, %d failed, %d skipped (required: %d passed, %d failed)\n",
		s.Passed, s.Failed, s.Skipped, s.RequiredPassed, s.RequiredFailed,
	)
	return nil
}

func main() {
	ctx := kong.Parse(&cli{},
		kong.Name("tools"),
		kong.Description("Development helper commands for Emissary."),
		kong.UsageOnError(),
	)
	ctx.FatalIfErrorf(ctx.Run())
}
