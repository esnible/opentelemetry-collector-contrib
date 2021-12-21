// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package oauth2clientauthextension // import "github.com/open-telemetry/opentelemetry-collector-contrib/extension/oauth2clientauthextension"

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configauth"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc/credentials"
	grpcOAuth "google.golang.org/grpc/credentials/oauth"
)

// ClientCredentialsAuthenticator provides implementation for providing client authentication using OAuth2 client credentials
// workflow for both gRPC and HTTP clients.
type ClientCredentialsAuthenticator struct {
	clientCredentials *clientcredentials.Config
	logger            *zap.Logger
	client            *http.Client
}

// ClientCredentialsAuthenticator implements ClientAuthenticator
var _ configauth.ClientAuthenticator = (*ClientCredentialsAuthenticator)(nil)

type errorWrappingTokenSource struct {
	ts     oauth2.TokenSource
	config *clientcredentials.Config
}

// errorWrappingTokenSource implements TokenSource
var _ oauth2.TokenSource = (*errorWrappingTokenSource)(nil)

// FailedToGetSecurityToken indicates a problem communicating with OAuth2 server.
// We support Unwrap() instead of using `%w` so that we can customize the error message
// to include both the wrapped error and information from the configuration.
type FailedToGetSecurityTokenError struct {
	inner  error
	config *clientcredentials.Config
}

func newClientCredentialsExtension(cfg *Config, logger *zap.Logger) (*ClientCredentialsAuthenticator, error) {
	if cfg.ClientID == "" {
		return nil, errNoClientIDProvided
	}
	if cfg.ClientSecret == "" {
		return nil, errNoClientSecretProvided
	}
	if cfg.TokenURL == "" {
		return nil, errNoTokenURLProvided
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()

	tlsCfg, err := cfg.TLSSetting.LoadTLSConfig()
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsCfg

	return &ClientCredentialsAuthenticator{
		clientCredentials: &clientcredentials.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			TokenURL:     cfg.TokenURL,
			Scopes:       cfg.Scopes,
		},
		logger: logger,
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
	}, nil
}

// Start for ClientCredentialsAuthenticator extension does nothing
func (o *ClientCredentialsAuthenticator) Start(_ context.Context, _ component.Host) error {
	return nil
}

// Shutdown for ClientCredentialsAuthenticator extension does nothing
func (o *ClientCredentialsAuthenticator) Shutdown(_ context.Context) error {
	return nil
}

func (ewts errorWrappingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := ewts.ts.Token()
	if err != nil {
		err = FailedToGetSecurityTokenError{
			inner:  err,
			config: ewts.config,
		}
	}
	return tok, err
}

// RoundTripper returns oauth2.Transport, an http.RoundTripper that performs "client-credential" OAuth flow and
// also auto refreshes OAuth tokens as needed.
func (o *ClientCredentialsAuthenticator) RoundTripper(base http.RoundTripper) (http.RoundTripper, error) {
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, o.client)
	return &oauth2.Transport{
		Source: errorWrappingTokenSource{
			ts:     o.clientCredentials.TokenSource(ctx),
			config: o.clientCredentials,
		},
		Base: base,
	}, nil
}

// PerRPCCredentials returns gRPC PerRPCCredentials that supports "client-credential" OAuth flow. The underneath
// oauth2.clientcredentials.Config instance will manage tokens performing auto refresh as necessary.
func (o *ClientCredentialsAuthenticator) PerRPCCredentials() (credentials.PerRPCCredentials, error) {
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, o.client)
	return grpcOAuth.TokenSource{
		TokenSource: errorWrappingTokenSource{
			ts:     o.clientCredentials.TokenSource(ctx),
			config: o.clientCredentials,
		},
	}, nil
}

// Error() marks ErrFailedToGetSecurityToken as an `error` type
func (e FailedToGetSecurityTokenError) Error() string {
	if e.config == nil {
		return "unconfigured ErrFailedToGetSecurityToken"
	}

	return fmt.Sprintf("failed to get security token from token endpoint %q: %v", e.config.TokenURL, e.inner)
}

// Unwrap() lets ErrFailedToGetSecurityToken work with errors.Is() and errors.As()
func (e FailedToGetSecurityTokenError) Unwrap() error {
	return e.inner
}
