/*
Copyright SecureKey Technologies Inc. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package operation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/btcsuite/btcutil/base58"
	"github.com/coreos/go-oidc"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/hyperledger/aries-framework-go/pkg/doc/signature/jsonld"
	"github.com/hyperledger/aries-framework-go/pkg/doc/signature/suite"
	"github.com/hyperledger/aries-framework-go/pkg/doc/signature/suite/ed25519signature2018"
	"github.com/hyperledger/aries-framework-go/pkg/doc/util"
	"github.com/hyperledger/aries-framework-go/pkg/doc/verifiable"
	"github.com/hyperledger/aries-framework-go/spi/storage"
	"github.com/piprate/json-gold/ld"
	"github.com/square/go-jose/jwt"
	"github.com/trustbloc/edge-core/pkg/log"
	"github.com/trustbloc/vcs/pkg/doc/vc/status/csl"
	edgesvcops "github.com/trustbloc/vcs/pkg/restapi/issuer/operation"
	vcprofile "github.com/trustbloc/vcs/pkg/storage"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/trustbloc/sandbox/pkg/internal/common/support"
	oidcclient "github.com/trustbloc/sandbox/pkg/restapi/internal/common/oidc"
	"github.com/trustbloc/sandbox/pkg/token"
)

const (
	login                     = "/login"
	settings                  = "/settings"
	getCreditScore            = "/getCreditScore"
	callback                  = "/callback"
	generate                  = "/generate"
	revoke                    = "/revoke"
	didcommInit               = "/didcomm/init"
	didcommToken              = "/didcomm/token"
	didcommCallback           = "/didcomm/cb"
	didcommCredential         = "/didcomm/data"
	didcommAssuranceData      = "/didcomm/assurance"
	didcommUserEndpoint       = "/didcomm/uid"
	oauth2GetRequestPath      = "/oauth2/request"
	oauth2CallbackPath        = "/oauth2/callback"
	oauth2TokenRequestPath    = "oauth2/token" //nolint:gosec
	verifyDIDAuthPath         = "/verify/didauth"
	createCredentialPath      = "/credential"
	authPath                  = "/auth"
	preAuthorizePath          = "/pre-authorize"
	authCodeFlowPath          = "/auth-code-flow"
	openID4CIWebhookCheckPath = "/verify/openid4ci/webhook/check"
	openID4CIWebhookPath      = "/verify/openid4ci/webhook"
	searchPath                = "/search"
	generateCredentialPath    = createCredentialPath + "/generate"
	oidcRedirectPath          = "/oidc/redirect" + "/{id}"

	oidcIssuanceLogin            = "/oidc/login"
	oidcIssuerIssuance           = "/oidc/issuance"
	oidcIssuanceOpenID           = "/{id}/.well-known/openid-configuration"
	oidcIssuanceAuthorize        = "/{id}/oidc/authorize"
	oidcIssuanceAuthorizeRequest = "/oidc/authorize-request"
	//nolint: gosec
	oidcIssuanceToken      = "/{id}/oidc/token"
	oidcIssuanceCredential = "/{id}/oidc/credential"

	// http query params
	stateQueryParam = "state"

	credentialContext = "https://www.w3.org/2018/credentials/v1"

	vcsUpdateStatusURLFormat = "%s/%s" + "/credentials/status"

	vcsProfileCookie     = "vcsProfile"
	scopeCookie          = "scopeCookie"
	adapterProfileCookie = "adapterProfile"
	assuranceScopeCookie = "assuranceScope"
	callbackURLCookie    = "callbackURL"

	issueCredentialURLFormat = "%s/%s" + "/credentials/issue"

	// contexts
	trustBlocExampleContext = "https://trustbloc.github.io/context/vc/examples-ext-v1.jsonld"
	citizenshipContext      = "https://w3id.org/citizenship/v1"

	vcsIssuerRequestTokenName = "vcs_issuer"

	// store
	txnStoreName = "issuer_txn"

	scopeQueryParam         = "scope"
	externalScopeQueryParam = "subject_data"
)

// Mock signer for signing VCs.
const (
	pkBase58 = "2MP5gWCnf67jvW3E4Lz8PpVrDWAXMYY1sDxjnkEnKhkkbKD7yP2mkVeyVpu5nAtr3TeDgMNjBPirk2XcQacs3dvZ"
	kid      = "did:key:z6MknC1wwS6DEYwtGbZZo2QvjQjkh2qSBjb4GYmbye8dv4S5#z6MknC1wwS6DEYwtGbZZo2QvjQjkh2qSBjb4GYmbye8dv4S5"
)

var logger = log.New("sandbox-issuer-restapi")

// Handler http handler for each controller API endpoint
type Handler interface {
	Path() string
	Method() string
	Handle() http.HandlerFunc
}

type oidcClient interface {
	CreateOIDCRequest(state, scope string) (string, error)
	HandleOIDCCallback(reqContext context.Context, code string) ([]byte, error)
}

// Operation defines handlers for authorization service
type Operation struct {
	handlers                 []Handler
	tokenIssuer              tokenIssuer
	extTokenIssuer           tokenIssuer
	tokenResolver            tokenResolver
	documentLoader           ld.DocumentLoader
	cmsURL                   string
	vcsURL                   string
	walletURL                string
	receiveVCHTML            string
	didAuthHTML              string
	vcHTML                   string
	didCommHTML              string
	didCommVpHTML            string
	httpClient               *http.Client
	requestTokens            map[string]string
	issuerAdapterURL         string
	store                    storage.Store
	oidcClient               oidcClient
	externalDataSourceURL    string
	externalAuthClientID     string
	externalAuthClientSecret string
	externalAuthProviderURL  string
	homePage                 string
	didcommScopes            map[string]struct{}
	assuranceScopes          map[string]string
	tlsConfig                *tls.Config
	externalLogin            bool
	preAuthorizeHTML         string
	authCodeFlowHTML         string

	vcsAPIAccessTokenHost         string
	vcsAPIAccessTokenClientID     string
	vcsAPIAccessTokenClientSecret string
	vcsAPIAccessTokenClaim        string
	vcsAPIURL                     string
	vcsClaimDataURL               string
	vcsDemoIssuer                 string
	eventsTopic                   *EventsTopic
}

// Config defines configuration for issuer operations
type Config struct {
	TokenIssuer              tokenIssuer
	ExtTokenIssuer           tokenIssuer
	TokenResolver            tokenResolver
	DocumentLoader           ld.DocumentLoader
	CMSURL                   string
	VCSURL                   string
	WalletURL                string
	ReceiveVCHTML            string
	DIDAuthHTML              string
	VCHTML                   string
	PreAuthorizeHTML         string
	AuthCodeFlowHTML         string
	DIDCommHTML              string
	DIDCOMMVPHTML            string
	TLSConfig                *tls.Config
	RequestTokens            map[string]string
	IssuerAdapterURL         string
	StoreProvider            storage.Provider
	OIDCProviderURL          string
	OIDCClientID             string
	OIDCClientSecret         string
	OIDCCallbackURL          string
	ExternalDataSourceURL    string
	ExternalAuthProviderURL  string
	ExternalAuthClientID     string
	ExternalAuthClientSecret string
	didcommScopes            map[string]struct{}
	assuranceScopes          map[string]string
	externalLogin            bool

	VcsAPIAccessTokenHost         string
	VcsAPIAccessTokenClientID     string
	VcsAPIAccessTokenClientSecret string
	VcsAPIAccessTokenClaim        string
	VcsAPIURL                     string
	VcsClaimDataURL               string
	VcsDemoIssuer                 string
}

// vc struct used to return vc data to html
type vc struct {
	Msg  string `json:"msg"`
	Data string `json:"data"`
}

type initiate struct {
	URL         string `json:"url"`
	TxID        string `json:"txID"`
	SuccessText string `json:"successText"`
	Pin         string `json:"pin"`
}

type clientCredentialsTokenResponseStruct struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

type tokenIssuer interface {
	AuthCodeURL(w http.ResponseWriter) string
	Exchange(r *http.Request) (*oauth2.Token, error)
	Client(t *oauth2.Token) *http.Client
}

type tokenResolver interface {
	Resolve(token string) (*token.Introspection, error)
}

type createOIDCRequestResponse struct {
	Request string `json:"request"`
}

// New returns authorization instance
func New(config *Config) (*Operation, error) { //nolint:funlen
	store, err := getTxnStore(config.StoreProvider)
	if err != nil {
		return nil, fmt.Errorf("issuer store provider : %w", err)
	}

	svc := &Operation{
		tokenIssuer:                   config.TokenIssuer,
		extTokenIssuer:                config.ExtTokenIssuer,
		tokenResolver:                 config.TokenResolver,
		documentLoader:                config.DocumentLoader,
		cmsURL:                        config.CMSURL,
		vcsURL:                        config.VCSURL,
		walletURL:                     config.WalletURL,
		receiveVCHTML:                 config.ReceiveVCHTML,
		didAuthHTML:                   config.DIDAuthHTML,
		vcHTML:                        config.VCHTML,
		didCommHTML:                   config.DIDCommHTML,
		didCommVpHTML:                 config.DIDCOMMVPHTML,
		httpClient:                    &http.Client{Transport: &http.Transport{TLSClientConfig: config.TLSConfig}},
		requestTokens:                 config.RequestTokens,
		issuerAdapterURL:              config.IssuerAdapterURL,
		store:                         store,
		externalDataSourceURL:         config.ExternalDataSourceURL,
		externalAuthClientID:          config.ExternalAuthClientID,
		externalAuthClientSecret:      config.ExternalAuthClientSecret,
		externalAuthProviderURL:       config.ExternalAuthProviderURL,
		homePage:                      config.OIDCCallbackURL,
		didcommScopes:                 map[string]struct{}{},
		assuranceScopes:               map[string]string{},
		tlsConfig:                     config.TLSConfig,
		externalLogin:                 config.externalLogin,
		preAuthorizeHTML:              config.PreAuthorizeHTML,
		authCodeFlowHTML:              config.AuthCodeFlowHTML,
		vcsAPIAccessTokenHost:         config.VcsAPIAccessTokenHost,
		vcsAPIAccessTokenClientID:     config.VcsAPIAccessTokenClientID,
		vcsAPIAccessTokenClientSecret: config.VcsAPIAccessTokenClientSecret,
		vcsAPIAccessTokenClaim:        config.VcsAPIAccessTokenClaim,
		vcsAPIURL:                     config.VcsAPIURL,
		vcsClaimDataURL:               config.VcsClaimDataURL,
		vcsDemoIssuer:                 config.VcsDemoIssuer,
		eventsTopic:                   NewEventsTopic(),
	}

	if config.didcommScopes != nil {
		svc.didcommScopes = config.didcommScopes
	}

	if config.assuranceScopes != nil {
		svc.assuranceScopes = config.assuranceScopes
	}

	if config.OIDCProviderURL != "" {
		svc.oidcClient, err = oidcclient.New(&oidcclient.Config{
			OIDCClientID:     config.OIDCClientID,
			OIDCClientSecret: config.OIDCClientSecret, OIDCCallbackURL: config.OIDCCallbackURL,
			OIDCProviderURL: config.OIDCProviderURL, TLSConfig: config.TLSConfig,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create oidc client : %w", err)
		}
	}

	svc.registerHandler()

	return svc, nil
}

// registerHandler register handlers to be exposed from this service as REST API endpoints
func (c *Operation) registerHandler() {
	// Add more protocol endpoints here to expose them as controller API endpoints
	c.handlers = []Handler{
		support.NewHTTPHandler(login, http.MethodGet, c.login),
		support.NewHTTPHandler(settings, http.MethodGet, c.settings),
		support.NewHTTPHandler(getCreditScore, http.MethodGet, c.getCreditScore),
		support.NewHTTPHandler(callback, http.MethodGet, c.callback),
		support.NewHTTPHandler(oidcRedirectPath, http.MethodGet, c.oidcRedirect),

		// issuer rest apis (html decoupled)
		support.NewHTTPHandler(authPath, http.MethodGet, c.auth),
		support.NewHTTPHandler(searchPath, http.MethodGet, c.search),
		support.NewHTTPHandler(verifyDIDAuthPath, http.MethodPost, c.verifyDIDAuthHandler),
		support.NewHTTPHandler(createCredentialPath, http.MethodPost, c.createCredentialHandler),
		support.NewHTTPHandler(generateCredentialPath, http.MethodPost, c.generateCredentialHandler),

		// chapi
		support.NewHTTPHandler(revoke, http.MethodPost, c.revokeVC),
		support.NewHTTPHandler(generate, http.MethodPost, c.generateVC),

		// oidc4ci authorize & pre-authorize
		support.NewHTTPHandler(preAuthorizePath, http.MethodGet, c.preAuthorize),
		support.NewHTTPHandler(authCodeFlowPath, http.MethodGet, c.authCodeFlowHandler),

		// webhooks
		support.NewHTTPHandler(openID4CIWebhookPath, http.MethodPost, c.eventsTopic.receiveTopics),
		support.NewHTTPHandler(openID4CIWebhookCheckPath, http.MethodGet, c.eventsTopic.checkTopics),

		// didcomm
		support.NewHTTPHandler(didcommToken, http.MethodPost, c.didcommTokenHandler),
		support.NewHTTPHandler(didcommCallback, http.MethodGet, c.didcommCallbackHandler),
		support.NewHTTPHandler(didcommCredential, http.MethodPost, c.didcommCredentialHandler),
		support.NewHTTPHandler(didcommAssuranceData, http.MethodPost, c.didcommAssuraceHandler),
		support.NewHTTPHandler(didcommInit, http.MethodGet, c.initiateDIDCommConnection),
		support.NewHTTPHandler(didcommUserEndpoint, http.MethodGet, c.getIDHandler),

		// oidc
		support.NewHTTPHandler(oauth2GetRequestPath, http.MethodGet, c.createOIDCRequest),
		support.NewHTTPHandler(oauth2CallbackPath, http.MethodGet, c.handleOIDCCallback),

		// oidc issuance
		support.NewHTTPHandler(oidcIssuerIssuance, http.MethodPost, c.initiateIssuance),
		support.NewHTTPHandler(oidcIssuanceOpenID, http.MethodGet, c.wellKnownConfiguration),
		support.NewHTTPHandler(oidcIssuanceAuthorize, http.MethodGet, c.oidcAuthorize),
		support.NewHTTPHandler(oidcIssuanceAuthorizeRequest, http.MethodPost, c.oidcSendAuthorizeResponse),
		support.NewHTTPHandler(oidcIssuanceToken, http.MethodPost, c.oidcTokenEndpoint),
		support.NewHTTPHandler(oidcIssuanceCredential, http.MethodPost, c.oidcCredentialEndpoint),
	}
}

// login using oauth2, will redirect to Auth Code URL
func (c *Operation) login(w http.ResponseWriter, r *http.Request) {
	var u string

	scope := r.URL.Query()["scope"]
	extAuthURL := c.extTokenIssuer.AuthCodeURL(w)

	if len(scope) > 0 {
		// If the scope is PermanentResidentCard but external auth url is not defined
		// then proceed with trustbloc login service
		if scope[0] == externalScopeQueryParam && !strings.Contains(extAuthURL, "EXTERNAL") {
			c.externalLogin = true
			u = c.extTokenIssuer.AuthCodeURL(w)
			u += "&scope=" + oidc.ScopeOpenID + " " + scope[0]
		} else {
			u = c.prepareAuthCodeURL(w, scope[0])
		}
	}

	expire := time.Now().AddDate(0, 0, 1)

	if len(r.URL.Query()["vcsProfile"]) == 0 {
		logger.Errorf("vcs profile is empty")
		c.writeErrorResponse(w, http.StatusBadRequest, "vcs profile is empty")

		return
	}

	cookie := http.Cookie{Name: vcsProfileCookie, Value: r.URL.Query()["vcsProfile"][0], Expires: expire}
	http.SetCookie(w, &cookie)

	http.SetCookie(w, &http.Cookie{Name: callbackURLCookie, Value: "", Expires: expire})

	http.Redirect(w, r, u, http.StatusTemporaryRedirect)
}

func (c *Operation) authCodeFlowHandler(w http.ResponseWriter, r *http.Request) {
	initiateReq := &initiateOIDC4CIRequest{
		CredentialTemplateID: "templateID",
		GrantType:            "authorization_code",
		ResponseType:         "code",
		Scope:                []string{"openid", "profile"},
		OpState:              uuid.New().String(),
		ClaimEndpoint:        c.vcsClaimDataURL,
	}

	c.buildInitiateOIDC4CIFlowPage(w, initiateReq, c.authCodeFlowHTML)
}

func (c *Operation) preAuthorize(w http.ResponseWriter, r *http.Request) {
	initiateReq := &initiateOIDC4CIRequest{
		CredentialTemplateID: "templateID",
		UserPinRequired:      true,
		ClaimData: &map[string]interface{}{
			"displayName":       "John Doe",
			"givenName":         "John",
			"jobTitle":          "Software Developer",
			"surname":           "Doe",
			"preferredLanguage": "English",
			"mail":              "john.doe@foo.bar",
			"photo":             "data:image/png;base64,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", //nolint
		},
	}

	if strings.EqualFold(r.URL.Query().Get("require_pin"), "false") {
		initiateReq.UserPinRequired = false
	}

	c.buildInitiateOIDC4CIFlowPage(w, initiateReq, c.preAuthorizeHTML)
}

func (c *Operation) buildInitiateOIDC4CIFlowPage( //nolint:funlen,gocyclo
	w http.ResponseWriter,
	initiateReq *initiateOIDC4CIRequest,
	htmlTemplate string,
) {
	accessToken, err := c.issueAccessToken(
		c.vcsAPIAccessTokenHost,
		c.vcsAPIAccessTokenClientID,
		c.vcsAPIAccessTokenClientSecret,
		[]string{c.vcsAPIAccessTokenClaim},
	)
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to get access token: %s", err.Error()))

		return
	}

	b, err := json.Marshal(initiateReq)
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to marshal: %s", err.Error()))

		return
	}

	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf(
			"%v/issuer/profiles/%v/interactions/initiate-oidc",
			c.vcsAPIURL,
			c.vcsDemoIssuer),
		bytes.NewBuffer(b),
	)
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("can not prepare http request: %s", err.Error()))

		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %v", accessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to send request for initiate: %s", err.Error()))

		return
	}

	if resp.Body != nil {
		defer func() {
			_ = resp.Body.Close() //nolint:errcheck
		}()
	}

	var parsedResp initiateOIDC4CIResponse

	if err = json.NewDecoder(resp.Body).Decode(&parsedResp); err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to decode initiate response: %s", err.Error()))

		return
	}

	pin := ""
	if parsedResp.UserPin != nil {
		pin = *parsedResp.UserPin
	}

	var successText strings.Builder
	successText.WriteString(fmt.Sprintf("Credentials with template [%v] and type [%v] ",
		initiateReq.CredentialTemplateID, "VerifiedEmployee"))

	if initiateReq.ClaimData != nil {
		successText.WriteString("and claims: ")
		for k, v := range *initiateReq.ClaimData {
			successText.WriteString(fmt.Sprintf("%v:%v ", k, v))
		}
	}
	successText.WriteString(fmt.Sprintf("was successfully issued by [%v]", c.vcsAPIAccessTokenClientID))

	t, err := template.ParseFiles(htmlTemplate)
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to load html: %s", err.Error()))

		return
	}

	if err = t.Execute(w, initiate{
		URL:         parsedResp.OfferCredentialURL,
		TxID:        parsedResp.TxID,
		SuccessText: successText.String(),
		Pin:         pin,
	}); err != nil {
		logger.Errorf(fmt.Sprintf("execute html template: %s", err.Error()))
	}
}

func (c *Operation) issueAccessToken(oidcProviderURL, clientID, secret string, scopes []string) (string, error) {
	conf := clientcredentials.Config{
		TokenURL:     oidcProviderURL + "/oauth2/token",
		ClientID:     clientID,
		ClientSecret: secret,
		Scopes:       scopes,
		AuthStyle:    oauth2.AuthStyleInHeader,
	}

	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, c.httpClient)

	tokenResult, err := conf.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}

	return tokenResult.AccessToken, nil
}

func (c *Operation) auth(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query()["scope"]
	if len(scope) == 0 {
		c.writeErrorResponse(w, http.StatusBadRequest, "scope is mandatory")

		return
	}

	callBackURL := r.URL.Query()["callbackURL"]

	if len(callBackURL) == 0 {
		c.writeErrorResponse(w, http.StatusBadRequest, "callbackURL is mandatory")

		return
	}

	referrer := r.URL.Query()["referrer"]

	if len(referrer) == 0 {
		c.writeErrorResponse(w, http.StatusBadRequest, "referrer is mandatory")

		return
	}

	u := c.tokenIssuer.AuthCodeURL(w)
	u += "&scope=" + scope[0]

	cookie := http.Cookie{
		Name:    callbackURLCookie,
		Value:   callBackURL[0],
		Expires: time.Now().AddDate(0, 0, 1),
	}
	http.SetCookie(w, &cookie)

	http.Redirect(w, r, "/oidc/redirect/"+referrer[0]+"?url="+url.QueryEscape(u), http.StatusTemporaryRedirect)
}

func (c *Operation) oidcRedirect(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query()["url"]
	if len(u) == 0 {
		c.writeErrorResponse(w, http.StatusBadRequest, "url is mandatory")

		return
	}

	const redirectHTML = `
	<!DOCTYPE html>
	<html>
	<head>
	  <meta name="referrer" content="no-referrer-when-downgrade"/>
	  <meta http-equiv="refresh" content="0; url='%s'" />
	</head>
	</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, redirectHTML, u[0])
}

func (c *Operation) search(w http.ResponseWriter, r *http.Request) {
	txnID := r.URL.Query()["txnID"]
	if len(txnID) == 0 {
		c.writeErrorResponse(w, http.StatusBadRequest, "txnID is mandatory")

		return
	}

	dataBytes, err := c.store.Get(txnID[0])
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("failed to get txn data: %s", err.Error()))

		return
	}

	logger.Infof("preview : sessionData=%s", string(dataBytes))

	var data *txnData

	err = json.Unmarshal(dataBytes, &data)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("unmarshal session data: %s", err.Error()))

		return
	}

	// TODO enhance the api to support dynamic search
	userData, err := c.getCMSUserData(strings.ToLower(data.Scope)+"s", data.UserID, data.Token)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get user data : %s", err.Error()))

		return
	}

	userDatabytes, err := json.Marshal(&searchData{
		Scope:    data.Scope,
		UserData: userData,
	})
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal user data : %s", err.Error()))

		return
	}

	keyID := uuid.NewString()

	err = c.store.Put(keyID, userDatabytes)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get assurance data : %s", err.Error()))

		return
	}

	c.writeResponse(w, http.StatusOK, []byte(fmt.Sprintf(`{"id" : "%s"}`, keyID)))
}

// initiateDIDCommConnection initiates a DIDComm connection from the issuer to the user's wallet
func (c *Operation) initiateDIDCommConnection(w http.ResponseWriter, r *http.Request) {
	issuerID := r.FormValue("adapterProfile")
	if issuerID == "" {
		logger.Errorf("missing adapterProfile")
		c.writeErrorResponse(w, http.StatusBadRequest, "missing adapterProfile")

		return
	}

	scope := r.FormValue("didCommScope")
	if scope == "" {
		logger.Errorf("missing didCommScope")
		c.writeErrorResponse(w, http.StatusBadRequest, "missing didCommScope")

		return
	}

	assuranceScope := r.FormValue("assuranceScope")

	c.didcommScopes[scope] = struct{}{}

	if assuranceScope != "" {
		c.assuranceScopes[scope] = assuranceScope
	}

	rURL := fmt.Sprintf("%s/%s/connect/wallet?cred=%s", c.issuerAdapterURL, issuerID, scope)
	http.Redirect(w, r, rURL, http.StatusFound)
}

func (c *Operation) hasAccessToken(r *http.Request) bool {
	authHeader := r.Header.Get("Authorization")
	return strings.HasPrefix(authHeader, "Bearer ")
}

func (c *Operation) getTokenInfo(w http.ResponseWriter, r *http.Request) (*token.Introspection, *oauth2.Token, error) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		logger.Infof("rejected request lacking Bearer token")
		w.Header().Add("WWW-Authenticate", "Bearer")
		w.WriteHeader(http.StatusUnauthorized)

		return nil, nil, fmt.Errorf("missing bearer token")
	}

	accessToken := strings.TrimPrefix(authHeader, "Bearer ")

	tk := oauth2.Token{AccessToken: accessToken}

	info, err := c.tokenResolver.Resolve(accessToken)
	if err != nil {
		logger.Errorf("failed to get token info: %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get token info: %s", err.Error()))

		return nil, nil, fmt.Errorf("\"failed to get token info: %w", err)
	}

	if !info.Active {
		logger.Infof("rejected request with invalid token")
		c.writeErrorResponse(w, http.StatusUnauthorized, `Bearer error="invalid_token"`)

		return nil, nil, fmt.Errorf("token is invalid")
	}

	return info, &tk, nil
}

func (c *Operation) getIDHandler(w http.ResponseWriter, r *http.Request) {
	info, tk, err := c.getTokenInfo(w, r)
	if err != nil {
		return
	}

	user, err := c.getCMSUser(tk, "email="+info.Subject)
	if err != nil {
		logger.Errorf("failed to get cms user: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get cms user: %s", err.Error()))

		return
	}

	resp := adapterTokenResp{
		UserID: user.UserID,
	}

	respBytes, err := json.Marshal(resp)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal userID response : %s", err.Error()))
		return
	}

	c.writeResponse(w, http.StatusOK, respBytes)
}

func (c *Operation) getDIDCommScopes(scopes string) []string {
	var out []string

	for _, scope := range strings.Split(scopes, " ") {
		if _, ok := c.didcommScopes[scope]; ok {
			out = append(out, scope)
		}
	}

	return out
}

// getCredentialUsingAccessToken services offline credential requests using an Oauth2 Bearer access token
func (c *Operation) getCredentialUsingAccessToken(w http.ResponseWriter, r *http.Request) {
	info, tk, err := c.getTokenInfo(w, r)
	if err != nil {
		return
	}

	scopes := strings.Join(c.getDIDCommScopes(info.Scope), " ")
	if scopes == "" {
		logger.Errorf("no valid credential scope")
		c.writeErrorResponse(w, http.StatusInternalServerError, "no valid credential scope")

		return
	}

	_, subjectData, err := c.getCMSData(tk, "email="+info.Subject, scopes)
	if err != nil {
		logger.Errorf("failed to get cms data: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get cms data: %s", err.Error()))

		return
	}

	delete(subjectData, "vcmetadata")
	delete(subjectData, "vccredentialsubject")

	subjectDataBytes, err := json.Marshal(subjectData)
	if err != nil {
		logger.Errorf("failed to marshal subject data: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal subject data: %s", err.Error()))

		return
	}

	c.writeResponse(w, http.StatusOK, subjectDataBytes)
}

// getAssuranceUsingAccessToken services offline assurance requests using an Oauth2 Bearer access token
func (c *Operation) getAssuranceUsingAccessToken(w http.ResponseWriter, r *http.Request) {
	info, tk, err := c.getTokenInfo(w, r)
	if err != nil {
		return
	}

	user, err := c.getCMSUser(tk, "email="+info.Subject)
	if err != nil {
		logger.Errorf("failed to get cms user: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get cms user: %s", err.Error()))

		return
	}

	scopes := c.getDIDCommScopes(info.Scope)

	assuranceScope := ""

	for _, scope := range scopes {
		if s, ok := c.assuranceScopes[scope]; ok {
			assuranceScope = s
			break
		}
	}

	if assuranceScope == "" {
		logger.Errorf("no assurance scope for credential scopes %v", scopes)
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("no assurance scope for credential scopes %v", scopes))

		return
	}

	assuranceData, err := c.getCMSUserData(assuranceScope, user.UserID, "")
	if err != nil {
		logger.Errorf("failed to get assurance data : %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get assurance data : %s", err.Error()))

		return
	}

	dataBytes, err := json.Marshal(assuranceData)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal assurance data : %s", err.Error()))

		return
	}

	c.writeResponse(w, http.StatusOK, dataBytes)
}

func (c *Operation) settings(w http.ResponseWriter, r *http.Request) {
	u := c.homePage

	expire := time.Now().AddDate(0, 0, 1)

	if len(r.URL.Query()["vcsProfile"]) == 0 {
		logger.Errorf("vcs profile is empty")
		c.writeErrorResponse(w, http.StatusBadRequest, "vcs profile is empty")

		return
	}

	cookie := http.Cookie{Name: vcsProfileCookie, Value: r.URL.Query()["vcsProfile"][0], Expires: expire}
	http.SetCookie(w, &cookie)

	http.Redirect(w, r, u, http.StatusTemporaryRedirect)
}

// callback for oauth2 login
func (c *Operation) callback(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()["error"]) != 0 {
		if r.URL.Query()["error"][0] == "access_denied" {
			http.Redirect(w, r, c.homePage, http.StatusTemporaryRedirect)
		}
	}

	vcsProfileCookie, err := r.Cookie(vcsProfileCookie)
	if err != nil {
		logger.Errorf("failed to get cookie: %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get cookie: %s", err.Error()))

		return
	}

	if c.externalLogin {
		c.getDataFromExternalSource(w, r, externalScopeQueryParam, vcsProfileCookie.Value)
	} else {
		c.getDataFromCms(w, r, vcsProfileCookie.Value)
	}
}

func (c *Operation) getDataFromExternalSource(w http.ResponseWriter, r *http.Request, scope, //nolint: funlen
	vcsCookie string) {
	tk, err := c.extTokenIssuer.Exchange(r)
	if err != nil {
		logger.Errorf("failed to exchange code for token: %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to exchange code for token: %s", err.Error()))

		return
	}
	// Fetching idToken from the token issuer
	idToken, ok := tk.Extra("id_token").(string)
	if !ok {
		logger.Errorf("failed to get id token: %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get id token: %s", err.Error()))

		return
	}

	subRefClaim, err := getSubjectReferenceClaim(idToken)
	if err != nil {
		logger.Errorf("failed to get subject reference claim: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get subject reference claim: %s", err.Error()))

		return
	}

	// get the access_token
	accessToken, err := c.getAccessToken(externalScopeQueryParam)
	if err != nil {
		logger.Errorf("failed to get access token: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get access token: %s", err.Error()))

		return
	}

	// get subject data from internal data source
	subjectData, err := c.getSubjectData(accessToken, subRefClaim)
	if err != nil {
		logger.Errorf("failed to get subject data from internal data source: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get subject data from internal data source: %s", err.Error()))

		return
	}

	// get the subject data and prepare credential
	cred, err := c.prepareCredential(subjectData, scope, vcsCookie)
	if err != nil {
		logger.Errorf("failed to create credential now: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create credential: %s", err.Error()))

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	t, err := template.ParseFiles(c.didAuthHTML)
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to load html: %s", err.Error()))

		return
	}

	if err := t.Execute(w, map[string]interface{}{
		"Path": generate + "?" + "profile=" + vcsCookie,
		"Cred": string(cred),
	}); err != nil {
		logger.Errorf(fmt.Sprintf("failed execute qr html template: %s", err.Error()))
	}
}

func (c *Operation) getDataFromCms(w http.ResponseWriter, r *http.Request, vcsCookie string) { //nolint: funlen,gocyclo
	tk, e := c.tokenIssuer.Exchange(r)
	if e != nil {
		logger.Errorf("failed to exchange code for token while getting data from cms : %s ", e.Error())
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to exchange code for token while getting data from cms : %s", e.Error()))

		return
	}
	// user info from token will be used for to retrieve data from cms
	info, err := c.tokenResolver.Resolve(tk.AccessToken)
	if err != nil {
		logger.Errorf("failed to get token info: %s and access token %s", err.Error(), tk.AccessToken)
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get token info: %s and access token %s", err.Error(), tk.AccessToken))

		return
	}

	userID, subject, err := c.getCMSData(tk, "email="+info.Subject, info.Scope)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get cms data: %s", err.Error()))

		return
	}

	callbackURLCookie, err := r.Cookie(callbackURLCookie)
	if err != nil && !errors.Is(err, http.ErrNoCookie) {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get authMode cookie: %s", err.Error()))

		return
	}

	if callbackURLCookie != nil && callbackURLCookie.Value != "" {
		txnID := uuid.NewString()
		data := txnData{
			UserID: userID,
			Scope:  info.Scope,
			Token:  tk.AccessToken,
		}

		dataBytes, mErr := json.Marshal(data)
		if mErr != nil {
			c.writeErrorResponse(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to marshal txn data: %s", mErr.Error()))
			return
		}

		err = c.store.Put(txnID, dataBytes)
		if err != nil {
			c.writeErrorResponse(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to save txn data: %s", err.Error()))

			return
		}

		http.Redirect(w, r, callbackURLCookie.Value+"?txnID="+txnID, http.StatusTemporaryRedirect)

		return
	}

	cred, err := c.prepareCredential(subject, info.Scope, vcsCookie)
	if err != nil {
		logger.Errorf("failed to create credential: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create credential: %s", err.Error()))

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	t, err := template.ParseFiles(c.didAuthHTML)
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to load html: %s", err.Error()))

		return
	}

	if err := t.Execute(w, map[string]interface{}{
		"Path": generate + "?" + "profile=" + vcsCookie,
		"Cred": string(cred),
	}); err != nil {
		logger.Errorf(fmt.Sprintf("failed execute qr html template: %s", err.Error()))
	}
}

func (c *Operation) getAccessToken(scope string) (string, error) {
	// call auth api to get access token
	req := url.Values{}
	req.Set("client_id", c.externalAuthClientID)
	req.Set("grant_type", "client_credentials")
	req.Set("scope", scope)
	reqBodyBytes := bytes.NewBuffer([]byte(req.Encode()))

	httpRequest, err := http.NewRequest("POST", c.externalAuthProviderURL+oauth2TokenRequestPath, reqBodyBytes)
	if err != nil {
		return "", fmt.Errorf("failed to post the request %w", err)
	}

	httpRequest.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	httpRequest.SetBasicAuth(url.QueryEscape(c.externalAuthClientID), url.QueryEscape(c.externalAuthClientSecret))

	resp, err := sendHTTPRequest(httpRequest, c.httpClient, http.StatusOK, "")
	if err != nil {
		return "", fmt.Errorf("failed to post the request to get the access token %w", err)
	}

	// unmarshal the response
	var tokenResponse clientCredentialsTokenResponseStruct

	err = json.Unmarshal(resp, &tokenResponse)
	if err != nil {
		return "", fmt.Errorf("error unmarshalling the token response %w", err)
	}

	return tokenResponse.AccessToken, nil
}

func (c *Operation) getSubjectData(accessToken, subRefClaim string) (subjectData map[string]interface{}, err error) {
	// pass access token to subjects/data?
	req, err := http.NewRequest("GET", c.externalDataSourceURL+"1.0/subjects/data?", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get subject data %w", err)
	}

	reqQuery := req.URL.Query()
	reqQuery.Add("aiid", "FiIJethCqaTkWh70Gq8D")
	reqQuery.Add("subjectReference", subRefClaim)
	req.URL.RawQuery = reqQuery.Encode()

	resp, err := sendHTTPRequest(req, c.httpClient, http.StatusOK, accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to send request to get the subject data %w", err)
	}

	err = json.Unmarshal(resp, &subjectData)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling the subject data  %w", err)
	}

	return subjectData, nil
}

func getSubjectReferenceClaim(idToken string) (string, error) {
	var claims jwt.Claims

	jwtToken, err := jwt.ParseSigned(idToken)
	if err != nil {
		return "", fmt.Errorf("failed to parse id token %w", err)
	}

	err = jwtToken.UnsafeClaimsWithoutVerification(&claims)
	if err != nil {
		return "", fmt.Errorf("failed to deserializes the claims of jwt %w", err)
	}

	return claims.Subject, nil
}

func (c *Operation) getCreditScore(w http.ResponseWriter, r *http.Request) {
	userID, subject, err := c.getCMSData(nil, "name="+url.QueryEscape(r.URL.Query()["givenName"][0]+" "+
		r.URL.Query()["familyName"][0]), r.URL.Query()["didCommScope"][0])
	if err != nil {
		logger.Errorf("failed to get cms data: %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get cms data: %s", err.Error()))

		return
	}

	c.didcomm(w, r, userID, subject, r.URL.Query()["adapterProfile"][0])
}

func (c *Operation) createOIDCRequest(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get(scopeQueryParam)
	if scope == "" {
		c.writeErrorResponse(w, http.StatusBadRequest, "missing scope")

		return
	}

	// TODO validate scope
	state := uuid.New().String()

	redirectURL, err := c.oidcClient.CreateOIDCRequest(state, scope)
	if err != nil {
		c.writeErrorResponse(w,
			http.StatusInternalServerError, fmt.Sprintf("failed to create oidc request : %s", err))

		return
	}

	response, err := json.Marshal(&createOIDCRequestResponse{
		Request: redirectURL,
	})
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError, fmt.Sprintf("failed to marshal response : %s", err))

		return
	}

	err = c.store.Put(state, []byte(state))
	if err != nil {
		c.writeErrorResponse(w,
			http.StatusInternalServerError, fmt.Sprintf("failed to write state to transient store : %s", err))

		return
	}

	w.Header().Set("content-type", "application/json")

	_, err = w.Write(response)
	if err != nil {
		logger.Errorf("failed to write response : %s", err)
	}
}

func (c *Operation) verifyDIDAuthHandler(w http.ResponseWriter, r *http.Request) {
	req := &verifyDIDAuthReq{}

	err := json.NewDecoder(r.Body).Decode(req)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to decode request: %s", err.Error()))

		return
	}

	err = c.validateAuthResp(req.DIDAuthResp, req.Holder, req.Domain, req.Challenge)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to validate did auth resp : %s", err.Error()))

		return
	}

	c.writeResponse(w, http.StatusOK, []byte(""))
}

func (c *Operation) createCredentialHandler(w http.ResponseWriter, r *http.Request) {
	req := &createCredentialReq{}

	err := json.NewDecoder(r.Body).Decode(req)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to decode request: %s", err.Error()))

		return
	}

	// get data from cms
	userData, err := c.getCMSUserData(req.Collection, req.UserID, "")
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get cms user data : %s", err.Error()))

		return
	}

	// support for dynamically adding subject data
	if len(req.CustomSubjectData) > 0 {
		if s, ok := userData["vccredentialsubject"]; ok && len(req.CustomSubjectData) > 0 {
			if subject, ok := s.(map[string]interface{}); ok {
				for k, v := range req.CustomSubjectData {
					subject[k] = v
				}
			}
		} else {
			userData["vccredentialsubject"] = req.CustomSubjectData
		}
	}

	// create credential
	cred, err := c.prepareCredential(userData, req.Scope, req.VCSProfile)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create credential: %s", err.Error()))

		return
	}

	signedVC, err := c.issueCredential(req.VCSProfile, req.Holder, cred)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to sign credential: %s", err.Error()))

		return
	}

	c.writeResponse(w, http.StatusOK, signedVC)
}

func (c *Operation) generateCredentialHandler(w http.ResponseWriter, r *http.Request) {
	req := &generateCredentialReq{}

	err := json.NewDecoder(r.Body).Decode(req)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to decode request: %s", err.Error()))

		return
	}

	dataBytes, err := c.store.Get(req.ID)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get user data using id '%s' : %s", req.ID, err.Error()))

		return
	}

	var sData *searchData

	err = json.Unmarshal(dataBytes, &sData)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to unmarshal user data : %s", err.Error()))

		return
	}

	// create credential
	cred, err := c.prepareCredential(sData.UserData, sData.Scope, req.VCSProfile)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create credential: %s", err.Error()))

		return
	}

	signedVC, err := c.issueCredential(req.VCSProfile, req.Holder, cred)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to sign credential: %s", err.Error()))

		return
	}

	c.writeResponse(w, http.StatusOK, signedVC)
}

func (c *Operation) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		logger.Errorf("missing state")
		c.didcommDemoResult(w, "missing state")

		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		logger.Errorf("missing code")
		c.didcommDemoResult(w, "missing code")

		return
	}

	_, err := c.store.Get(state)
	if errors.Is(err, storage.ErrDataNotFound) {
		logger.Errorf("invalid state parameter")
		c.didcommDemoResult(w, "invalid state parameter")

		return
	}

	if err != nil {
		logger.Errorf("failed to query transient store for state : %s", err)
		c.didcommDemoResult(w, fmt.Sprintf("failed to query transient store for state : %s", err))

		return
	}

	data, err := c.oidcClient.HandleOIDCCallback(r.Context(), code)
	if err != nil {
		logger.Errorf("failed to handle oidc callback : %s", err)
		c.didcommDemoResult(w, fmt.Sprintf("failed to handle oidc callback: %s", err))

		return
	}

	c.didcommDemoResult(w, string(data))
}

func (c *Operation) didcommDemoResult(w http.ResponseWriter, data string) {
	t, err := template.ParseFiles(c.didCommVpHTML)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to load html: %s", err.Error()))

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := t.Execute(w, vc{Data: data}); err != nil {
		logger.Errorf(fmt.Sprintf("failed execute html template: %s", err.Error()))
	}
}

// generateVC for creates VC
func (c *Operation) generateVC(w http.ResponseWriter, r *http.Request) {
	vcsProfileCookie, err := r.Cookie(vcsProfileCookie)
	if err != nil {
		logger.Errorf("failed to get vcsProfileCookie: %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to get cookie: %s", err.Error()))

		return
	}

	err = r.ParseForm()
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("failed to parse request form: %s", err.Error()))

		return
	}

	err = c.validateForm(r.Form, "cred", "holder", "authresp", "domain", "challenge")
	if err != nil {
		logger.Errorf("invalid generate credential request: %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid request argument: %s", err.Error()))

		return
	}

	cred, err := c.createCredential(r.Form["cred"][0], r.Form["authresp"][0], r.Form["holder"][0],
		r.Form["domain"][0], r.Form["challenge"][0], vcsProfileCookie.Value)
	if err != nil {
		logger.Errorf("failed to create verifiable credential: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to create verifiable credential: %s", err.Error()))

		return
	}

	err = c.storeCredential(cred, vcsProfileCookie.Value)
	if err != nil {
		logger.Errorf("failed to store credential: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to store credential: %s", err.Error()))

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	t, err := template.ParseFiles(c.receiveVCHTML)
	if err != nil {
		logger.Errorf(err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to load html: %s", err.Error()))

		return
	}

	if err := t.Execute(w, vc{Data: string(cred)}); err != nil {
		logger.Errorf(fmt.Sprintf("failed execute html template: %s", err.Error()))
	}
}

// revokeVC
func (c *Operation) revokeVC(w http.ResponseWriter, r *http.Request) { //nolint: funlen,gocyclo
	if err := r.ParseForm(); err != nil {
		logger.Errorf("failed to parse form: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to parse form: %s", err.Error()))

		return
	}

	vp, err := verifiable.ParsePresentation([]byte(r.Form.Get("vcDataInput")),
		verifiable.WithPresDisabledProofCheck(),
		verifiable.WithPresJSONLDDocumentLoader(c.documentLoader))
	if err != nil {
		logger.Errorf("failed to parse presentation: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to parse presentation: %s", err.Error()))

		return
	}

	for _, cred := range vp.Credentials() {
		cre, ok := cred.(map[string]interface{})
		if !ok {
			logger.Errorf("failed to cast credential")
			c.writeErrorResponse(w, http.StatusInternalServerError, "failed to cast credential")

			return
		}

		credBytes, errMarshal := json.Marshal(cre)
		if errMarshal != nil {
			logger.Errorf("failed to marshal credentials: %s", errMarshal.Error())
			c.writeErrorResponse(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to marshal credentials: %s", errMarshal.Error()))

			return
		}

		vc, errParse := verifiable.ParseCredential(credBytes, verifiable.WithDisabledProofCheck(),
			verifiable.WithJSONLDDocumentLoader(c.documentLoader))
		if errParse != nil {
			logger.Errorf("failed to parse credentials: %s", errParse.Error())
			c.writeErrorResponse(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to parse credentials: %s", errParse.Error()))

			return
		}

		reqBytes, errPrepare := prepareUpdateCredentialStatusRequest(vc)
		if errPrepare != nil {
			c.writeErrorResponse(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to prepare update credential status request: %s", errPrepare.Error()))

			return
		}

		endpointURL := fmt.Sprintf(vcsUpdateStatusURLFormat, c.vcsURL, vc.Issuer.CustomFields["name"].(string))

		req, errReq := http.NewRequest("POST", endpointURL,
			bytes.NewBuffer(reqBytes))
		if errReq != nil {
			logger.Errorf("failed to create new http request: %s", errReq.Error())
			c.writeErrorResponse(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to create new http request: %s", errReq.Error()))

			return
		}

		_, err = sendHTTPRequest(req, c.httpClient, http.StatusOK, c.requestTokens[vcsIssuerRequestTokenName])
		if err != nil {
			logger.Errorf("failed to update vc status: %s", err.Error())
			c.writeErrorResponse(w, http.StatusBadRequest,
				fmt.Sprintf("failed to update vc status: %s", err.Error()))

			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	t, err := template.ParseFiles(c.vcHTML)
	if err != nil {
		logger.Errorf("unable to load html: %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to load html: %s", err.Error()))

		return
	}

	if err := t.Execute(w, vc{Msg: "VC is revoked", Data: r.Form.Get("vcDataInput")}); err != nil {
		logger.Errorf(fmt.Sprintf("failed execute html template: %s", err.Error()))
	}
}

func (c *Operation) initiateIssuance(w http.ResponseWriter, r *http.Request) {
	oidcIssuanceReq := &oidcIssuanceRequest{}

	err := json.NewDecoder(r.Body).Decode(oidcIssuanceReq)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to decode request: %s", err.Error()))

		return
	}

	walletURL := oidcIssuanceReq.WalletInitIssuanceURL
	if walletURL == "" {
		walletURL = c.walletURL
	}

	credentialTypes := strings.Split(oidcIssuanceReq.CredentialTypes, ",")
	manifestIDs := strings.Split(oidcIssuanceReq.ManifestIDs, ",")
	issuerURL := oidcIssuanceReq.IssuerURL
	credManifest := oidcIssuanceReq.CredManifest
	credential := oidcIssuanceReq.Credential

	key := uuid.NewString()
	issuer := issuerURL + "/" + key

	issuerConf, err := json.MarshalIndent(&issuerConfiguration{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + "/oidc/authorize",
		TokenEndpoint:         issuer + "/oidc/token",
		CredentialEndpoint:    issuer + "/oidc/credential",
		CredentialManifests:   credManifest,
	}, "", "	")

	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to prepare issuer wellknown configuration : %s", err))

		return
	}

	err = c.saveIssuanceConfig(key, issuerConf, credential)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to store issuer server configuration : %s", err))

		return
	}

	redirectURL, err := parseWalletURL(walletURL, issuer, credentialTypes, manifestIDs)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to parse wallet init issuance URL : %s", err))

		return
	}

	c.writeResponse(w, http.StatusOK, []byte(redirectURL))
}

func parseWalletURL(walletURL, issuer string, credentialTypes, manifestIDs []string) (string, error) {
	u, err := url.Parse(walletURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse wallet init issuance URL : %w", err)
	}

	q := u.Query()
	q.Set("issuer", issuer)

	for _, credType := range credentialTypes {
		q.Add("credential_type", credType)
	}

	for _, manifestID := range manifestIDs {
		q.Add("manifest_id", manifestID)
	}

	u.RawQuery = q.Encode()

	return u.String(), nil
}

func (c *Operation) saveIssuanceConfig(key string, issuerConf, credential []byte) error {
	err := c.store.Put(key, issuerConf)
	if err != nil {
		return fmt.Errorf("failed to store issuer server configuration : %w", err)
	}

	err = c.store.Put(getCredStoreKeyPrefix(key), credential)
	if err != nil {
		return fmt.Errorf("failed to store credential : %w", err)
	}

	return nil
}

func (c *Operation) wellKnownConfiguration(w http.ResponseWriter, r *http.Request) {
	enableCors(w)

	id := mux.Vars(r)["id"]

	issuerConf, err := c.store.Get(id)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to read well known configuration : %s", err))

		return
	}

	w.Header().Set("Content-Type", "application/json")
	c.writeResponse(w, http.StatusOK, issuerConf)
}

func (c *Operation) oidcAuthorize(w http.ResponseWriter, r *http.Request) { //nolint: funlen
	if err := r.ParseForm(); err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to parse request : %s", err))

		return
	}

	claims, err := url.PathUnescape(r.Form.Get("claims"))
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to read claims : %s", err))

		return
	}

	redirectURI, err := url.PathUnescape(r.Form.Get("redirect_uri"))
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest,
			fmt.Sprintf("failed to read redirect URI : %s", err))

		return
	}

	scope := r.Form.Get("scope")
	state := r.Form.Get("state")
	responseType := r.Form.Get("response_type")
	clientID := r.Form.Get("client_id")

	// basic validation only.
	if claims == "" || redirectURI == "" || clientID == "" || state == "" {
		c.writeErrorResponse(w, http.StatusBadRequest, "Invalid Request")

		return
	}

	authState := uuid.NewString()

	authRequest, err := json.Marshal(map[string]string{
		"claims":        claims,
		"scope":         scope,
		"state":         state,
		"response_type": responseType,
		"client_id":     clientID,
		"redirect_uri":  redirectURI,
	})
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to process authorization request : %s", err))

		return
	}

	err = c.store.Put(getAuthStateKeyPrefix(authState), authRequest)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to save state : %s", err))

		return
	}

	authStateCookie := http.Cookie{
		Name:    "state",
		Value:   authState,
		Expires: time.Now().Add(5 * time.Minute), //nolint: gomnd
		Path:    "/",
	}

	http.SetCookie(w, &authStateCookie)
	http.Redirect(w, r, oidcIssuanceLogin, http.StatusFound)
}

func (c *Operation) oidcSendAuthorizeResponse(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("state")
	if err != nil {
		c.writeErrorResponse(w, http.StatusForbidden, "invalid state")

		return
	}

	authRqstBytes, err := c.store.Get(getAuthStateKeyPrefix(stateCookie.Value))
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, "invalid request")

		return
	}

	var authRequest map[string]string

	err = json.Unmarshal(authRqstBytes, &authRequest)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError, "failed to read request")

		return
	}

	redirectURI, ok := authRequest["redirect_uri"]
	if !ok {
		c.writeErrorResponse(w, http.StatusInternalServerError, "failed to redirect, invalid URL")

		return
	}

	state, ok := authRequest["state"]
	if !ok {
		c.writeErrorResponse(w, http.StatusInternalServerError, "failed to redirect, invalid state")

		return
	}

	authCode := uuid.NewString()

	err = c.store.Put(getAuthCodeKeyPrefix(authCode), []byte(stateCookie.Value))
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError, "failed to store state cookie value")

		return
	}

	redirectTo := fmt.Sprintf("%s?code=%s&state=%s", redirectURI, authCode, state)

	// TODO process credential types or manifests from claims and prepare credential
	// endpoint with credential to be issued.
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func (c *Operation) oidcTokenEndpoint(w http.ResponseWriter, r *http.Request) {
	setOIDCResponseHeaders(w)

	code := r.FormValue("code")
	redirectURI := r.FormValue("redirect_uri")
	grantType := r.FormValue("grant_type")

	if grantType != "authorization_code" {
		c.sendOIDCErrorResponse(w, "unsupported grant type", http.StatusBadRequest)
		return
	}

	authState, err := c.store.Get(getAuthCodeKeyPrefix(code))
	if err != nil {
		c.sendOIDCErrorResponse(w, "invalid state", http.StatusBadRequest)
		return
	}

	authRqstBytes, err := c.store.Get(getAuthStateKeyPrefix(string(authState)))
	if err != nil {
		c.sendOIDCErrorResponse(w, "invalid request", http.StatusBadRequest)
		return
	}

	var authRequest map[string]string

	err = json.Unmarshal(authRqstBytes, &authRequest)
	if err != nil {
		c.sendOIDCErrorResponse(w, "failed to read request", http.StatusInternalServerError)
		return
	}

	if authRedirectURI := authRequest["redirect_uri"]; authRedirectURI != redirectURI {
		c.sendOIDCErrorResponse(w, "request validation failed", http.StatusInternalServerError)
		return
	}

	mockAccessToken := uuid.NewString()
	mockIssuerID := mux.Vars(r)["id"]

	err = c.store.Put(getAccessTokenKeyPrefix(mockAccessToken), []byte(mockIssuerID))
	if err != nil {
		c.sendOIDCErrorResponse(w, "failed to save token state", http.StatusInternalServerError)
		return
	}

	response, err := json.Marshal(map[string]interface{}{
		"token_type":   "Bearer",
		"access_token": mockAccessToken,
		"expires_in":   3600 * time.Second, //nolint: gomnd
	})
	// TODO add id_token, c_nonce, c_nonce_expires_in

	if err != nil {
		c.sendOIDCErrorResponse(w, "response_write_error", http.StatusBadRequest)

		return
	}

	c.writeResponse(w, http.StatusOK, response)
}

func (c *Operation) oidcCredentialEndpoint(w http.ResponseWriter, r *http.Request) { //nolint: funlen,gocyclo
	setOIDCResponseHeaders(w)

	// TODO read and validate credential 'type', useful in multiple credential download.
	format := r.FormValue("format")

	if format != "" && format != "ldp_vc" {
		c.sendOIDCErrorResponse(w, "unsupported format requested", http.StatusBadRequest)
		return
	}

	authHeader := strings.Split(r.Header.Get("Authorization"), "Bearer ")
	if len(authHeader) != 2 { //nolint: gomnd
		c.sendOIDCErrorResponse(w, "malformed token", http.StatusBadRequest)
		return
	}

	if authHeader[1] == "" {
		c.sendOIDCErrorResponse(w, "invalid token", http.StatusForbidden)
		return
	}

	mockIssuerID := mux.Vars(r)["id"]

	issuerID, err := c.store.Get(getAccessTokenKeyPrefix(authHeader[1]))
	if err != nil {
		c.sendOIDCErrorResponse(w, "unsupported format requested", http.StatusBadRequest)
		return
	}

	if mockIssuerID != string(issuerID) {
		c.sendOIDCErrorResponse(w, "invalid transaction", http.StatusForbidden)
		return
	}

	credentialBytes, err := c.store.Get(getCredStoreKeyPrefix(mockIssuerID))
	if err != nil {
		c.sendOIDCErrorResponse(w, "failed to get credential", http.StatusInternalServerError)
		return
	}

	docLoader := ld.NewDefaultDocumentLoader(nil)

	credential, err := verifiable.ParseCredential(credentialBytes, verifiable.WithJSONLDDocumentLoader(docLoader))
	if err != nil {
		c.sendOIDCErrorResponse(w, "failed to prepare credential", http.StatusInternalServerError)
		return
	}

	err = signVCWithED25519(credential, docLoader)
	if err != nil {
		c.sendOIDCErrorResponse(w, "failed to issue credential", http.StatusInternalServerError)
		return
	}

	credBytes, err := credential.MarshalJSON()
	if err != nil {
		c.sendOIDCErrorResponse(w, "failed to write credential bytes", http.StatusInternalServerError)
		return
	}

	response, err := json.Marshal(map[string]interface{}{
		"format":     format,
		"credential": json.RawMessage(credBytes),
	})
	// TODO add support for acceptance token & nonce for deferred flow.
	if err != nil {
		c.sendOIDCErrorResponse(w, "response_write_error", http.StatusBadRequest)
		return
	}

	c.writeResponse(w, http.StatusOK, response)
}

func setOIDCResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (c *Operation) sendOIDCErrorResponse(w http.ResponseWriter, msg string, status int) {
	w.WriteHeader(status)
	c.writeResponse(w, status, []byte(fmt.Sprintf(`{"error": "%s"}`, msg)))
}

func enableCors(w http.ResponseWriter) {
	(w).Header().Set("Access-Control-Allow-Origin", "*")
}

// didcomm redirects to the issuer-adapter so it connects to the wallet over DIDComm.
func (c *Operation) didcomm(w http.ResponseWriter, r *http.Request, userID string, subjectData map[string]interface{},
	issuerID string) {
	if issuerID == "" {
		adapterProfileCookie, err := r.Cookie(adapterProfileCookie)
		if err != nil {
			logger.Errorf("failed to get adapterProfileCookie: %s", err.Error())
			c.writeErrorResponse(w, http.StatusBadRequest,
				fmt.Sprintf("failed to get adapterProfileCookie: %s", err.Error()))

			return
		}

		issuerID = adapterProfileCookie.Value
	}

	subjectDataBytes, err := json.Marshal(subjectData)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to store state subject mapping : %s", err.Error()))
		return
	}

	assuranceScopeCookie, err := r.Cookie(assuranceScopeCookie)
	if err != nil && !errors.Is(err, http.ErrNoCookie) {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("failed to get assuranceScopeCookie: %s",
			err.Error()))

		return
	}

	userData := userDataMap{
		ID:   userID,
		Data: subjectDataBytes,
	}

	if assuranceScopeCookie != nil {
		userData.AssuranceScope = assuranceScopeCookie.Value
	}

	userDataBytes, err := json.Marshal(userData)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError, fmt.Sprintf("failed to marshal data : %s", err.Error()))

		return
	}

	state := uuid.New().String()

	err = c.store.Put(state, userDataBytes)
	if err != nil {
		logger.Errorf("failed to store state subject mapping : %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to store state subject mapping : %s", err.Error()))

		return
	}

	logger.Infof("didcomm user data : state=%s data=%s", state, string(userDataBytes))

	http.Redirect(w, r, fmt.Sprintf(c.issuerAdapterURL+"/%s/connect/wallet?state=%s", issuerID, state), http.StatusFound)
}

func (c *Operation) didcommTokenHandler(w http.ResponseWriter, r *http.Request) {
	data := &adapterTokenReq{}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		logger.Errorf("invalid request : %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %s", err.Error()))

		return
	}

	cred, err := c.store.Get(data.State)
	if err != nil {
		logger.Errorf("invalid state : %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid state : %s", err.Error()))

		return
	}

	tkn := uuid.New().String()

	err = c.store.Put(tkn, cred)
	if err != nil {
		logger.Errorf("failed to store adapter token and userID mapping : %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to store adapter token and userID mapping : %s", err.Error()))

		return
	}

	userInfo := userDataMap{}

	err = json.Unmarshal(cred, &userInfo)
	if err != nil {
		logger.Errorf("failed to unmarshal user state info : %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to read user state info : %s", err.Error()))

		return
	}

	resp := adapterTokenResp{
		Token:  tkn,
		UserID: userInfo.ID,
	}

	respBytes, err := json.Marshal(resp)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to store adapter token and userID mapping : %s", err.Error()))
		return
	}

	c.writeResponse(w, http.StatusOK, respBytes)

	logger.Infof("didcomm flow token creation : token:%s credential=%s", string(respBytes), string(cred))
}

func (c *Operation) didcommCallbackHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	t, err := template.ParseFiles(c.didCommHTML)
	if err != nil {
		logger.Errorf("unable to load didcomm html: %s", err)
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to load didcomm html: %s", err.Error()))

		return
	}

	err = t.Execute(w, map[string]interface{}{})
	if err != nil {
		logger.Errorf("failed execute didcomm html template: %s", err.Error())
	} else {
		logger.Infof("didcomm callback handler success")
	}
}

func (c *Operation) didcommCredentialHandler(w http.ResponseWriter, r *http.Request) {
	if c.hasAccessToken(r) {
		c.getCredentialUsingAccessToken(w, r)
		return
	}

	data := &adapterDataReq{}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		logger.Errorf("invalid request : %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %s", err.Error()))

		return
	}

	userDataBytes, err := c.store.Get(data.Token)
	if err != nil {
		logger.Errorf("failed to get token data : %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("failed to get token data : %s", err.Error()))

		return
	}

	var userData userDataMap

	err = json.Unmarshal(userDataBytes, &userData)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError, fmt.Sprintf("user data unmarshal failed: %s", err.Error()))

		return
	}

	logger.Infof("didcomm flow get user data : token:%s credential=%s", data.Token, string(userData.Data))

	c.writeResponse(w, http.StatusOK, userData.Data)
}

func (c *Operation) didcommAssuraceHandler(w http.ResponseWriter, r *http.Request) {
	if c.hasAccessToken(r) {
		c.getAssuranceUsingAccessToken(w, r)
		return
	}

	data := &adapterDataReq{}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		logger.Errorf("invalid request : %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %s", err.Error()))

		return
	}

	// make sure token exists
	userDataBytes, err := c.store.Get(data.Token)
	if err != nil {
		logger.Errorf("failed to get token data : %s", err.Error())
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("failed to get token data : %s", err.Error()))

		return
	}

	var userData userDataMap

	err = json.Unmarshal(userDataBytes, &userData)
	if err != nil {
		logger.Errorf("user data unmarshal failed : %s", err.Error())
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("user data unmarshal failed : %s", err.Error()))

		return
	}

	assuranceData, err := c.getCMSUserData(userData.AssuranceScope, userData.ID, "")
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get assurance data : %s", err.Error()))

		return
	}

	dataBytes, err := json.Marshal(assuranceData)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal assurance data : %s", err.Error()))

		return
	}

	logger.Infof("didcomm flow get assurance data : token:%s credential=%s", data.Token, string(dataBytes))

	c.writeResponse(w, http.StatusOK, dataBytes)
}

func (c *Operation) getCMSUserData(scope, userID, tkn string) (map[string]interface{}, error) {
	u := c.cmsURL + "/" + scope + "?userid=" + userID

	logger.Infof("url = %s", u)

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}

	subjectBytes, err := sendHTTPRequest(req, c.httpClient, http.StatusOK, tkn)
	if err != nil {
		return nil, err
	}

	return unmarshalSubject(subjectBytes)
}

func (c *Operation) validateAdapterCallback(redirectURL string) error {
	u, err := url.Parse(redirectURL)
	if err != nil {
		return fmt.Errorf("didcomm callback - error parsing the request url: %w", err)
	}

	state := u.Query().Get(stateQueryParam)
	if state == "" {
		return errors.New("missing state in http query param")
	}

	_, err = c.store.Get(state)
	if err != nil {
		return fmt.Errorf("invalid state : %w", err)
	}

	// TODO https://github.com/trustbloc/sandbox/issues/493 validate token existence for the state

	return nil
}

func (c *Operation) getCMSUser(tk *oauth2.Token, searchQuery string) (*cmsUser, error) {
	userURL := c.cmsURL + "/users?" + searchQuery

	httpClient := c.httpClient
	if tk != nil {
		httpClient = c.tokenIssuer.Client(tk)
	}

	req, err := http.NewRequest("GET", userURL, nil)
	if err != nil {
		return nil, err
	}

	userBytes, err := sendHTTPRequest(req, httpClient, http.StatusOK, "")
	if err != nil {
		return nil, err
	}

	return unmarshalUser(userBytes)
}

func unmarshalUser(userBytes []byte) (*cmsUser, error) {
	var users []cmsUser

	err := json.Unmarshal(userBytes, &users)
	if err != nil {
		return nil, err
	}

	if len(users) == 0 {
		return nil, errors.New("user not found")
	}

	if len(users) > 1 {
		return nil, errors.New("multiple users found")
	}

	return &users[0], nil
}

func unmarshalSubject(data []byte) (map[string]interface{}, error) {
	var subjects []map[string]interface{}

	err := json.Unmarshal(data, &subjects)
	if err != nil {
		return nil, err
	}

	if len(subjects) == 0 {
		return nil, errors.New("record not found")
	}

	if len(subjects) > 1 {
		return nil, errors.New("multiple records found")
	}

	return subjects[0], nil
}

func (c *Operation) prepareCredential(subject map[string]interface{}, scope, vcsProfile string) ([]byte, error) {
	// will be replaced by DID auth response subject ID
	subject["id"] = ""
	defaultCredTypes := []string{"VerifiableCredential", "PermanentResidentCard"}
	vcContext := []string{credentialContext, trustBlocExampleContext}
	customFields := make(map[string]interface{})
	// get custom vc data if available
	if m, ok := subject["vcmetadata"]; ok {
		if vcMetaData, ok := m.(map[string]interface{}); ok {
			vcContext = getCustomContext(vcContext, vcMetaData)
			customFields["name"] = vcMetaData["name"]
			customFields["description"] = vcMetaData["description"]
		}
	}

	// remove cms specific fields
	delete(subject, "created_at")
	delete(subject, "updated_at")
	delete(subject, "userid")
	delete(subject, "vcmetadata")

	profileResponse, err := c.retrieveProfile(vcsProfile)
	if err != nil {
		return nil, fmt.Errorf("retrieve profile - name=%s err=%w", vcsProfile, err)
	}

	cred := &verifiable.Credential{}
	// Todo ideally scope should be what need to passed as a type
	// but from external data source the scope is subject_data. Need to revisit this logic
	switch scope {
	case externalScopeQueryParam:
		cred.Types = defaultCredTypes
		cred.Subject = subject["subjectData"]
		customFields["name"] = "Permanent Resident Card"
		cred.Context = []string{credentialContext, citizenshipContext}
	default:
		cred.Types = []string{"VerifiableCredential", scope}
		cred.Context = vcContext
		cred.Subject = subject
	}

	cred.Issued = util.NewTime(time.Now().UTC())
	cred.Issuer.ID = profileResponse.DID
	cred.Issuer.CustomFields = make(verifiable.CustomFields)
	cred.Issuer.CustomFields["name"] = profileResponse.Name
	cred.ID = profileResponse.URI + "/" + uuid.New().String()
	cred.CustomFields = customFields

	// credential subject as single json entity in CMS for complex data
	if s, ok := subject["vccredentialsubject"]; ok {
		if subject, ok := s.(map[string]interface{}); ok {
			cred.Subject = subject
		}
	}

	return json.Marshal(cred)
}

func getCustomContext(existingContext []string, customCtx map[string]interface{}) []string {
	if ctx, found := customCtx["@context"]; found {
		var result []string
		for _, v := range ctx.([]interface{}) {
			result = append(result, v.(string))
		}

		return result
	}

	return existingContext
}

func (c *Operation) retrieveProfile(profileName string) (*vcprofile.IssuerProfile, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf(c.vcsURL+"/profile/%s", profileName), nil)
	if err != nil {
		return nil, err
	}

	respBytes, err := sendHTTPRequest(req, c.httpClient, http.StatusOK, c.requestTokens[vcsIssuerRequestTokenName])
	if err != nil {
		return nil, err
	}

	profileResponse := &vcprofile.IssuerProfile{}

	err = json.Unmarshal(respBytes, profileResponse)
	if err != nil {
		return nil, err
	}

	return profileResponse, nil
}

func (c *Operation) createCredential(cred, authResp, holder, domain, challenge, id string) ([]byte, error) { //nolint: lll
	err := c.validateAuthResp([]byte(authResp), holder, domain, challenge)
	if err != nil {
		return nil, fmt.Errorf("DID Auth failed: %w", err)
	}

	return c.issueCredential(id, holder, []byte(cred))
}

func (c *Operation) issueCredential(profileID, holder string, cred []byte) ([]byte, error) {
	credential, err := verifiable.ParseCredential(cred, verifiable.WithDisabledProofCheck(),
		verifiable.WithJSONLDDocumentLoader(c.documentLoader))
	if err != nil {
		return nil, fmt.Errorf("invalid credential: %w", err)
	}

	if subject, ok := credential.Subject.([]verifiable.Subject); ok && len(subject) > 0 {
		subject[0].ID = holder
	} else if subjectString, ok := credential.Subject.(string); ok {
		subject := make([]verifiable.Subject, 1)

		subject[0].ID = subjectString
	} else {
		return nil, errors.New("invalid credential subject")
	}

	credBytes, err := credential.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to get credential bytes: %w", err)
	}

	body, err := json.Marshal(edgesvcops.IssueCredentialRequest{
		Credential: credBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal credential")
	}

	endpointURL := fmt.Sprintf(issueCredentialURLFormat, c.vcsURL, profileID)

	req, err := http.NewRequest("POST", endpointURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	return sendHTTPRequest(req, c.httpClient, http.StatusCreated, c.requestTokens[vcsIssuerRequestTokenName])
}

// validateAuthResp validates did auth response against given domain and challenge
func (c *Operation) validateAuthResp(authResp []byte, holder, domain, challenge string) error { // nolint:gocyclo
	vp, err := verifiable.ParsePresentation(authResp, verifiable.WithPresDisabledProofCheck(),
		verifiable.WithPresJSONLDDocumentLoader(c.documentLoader))
	if err != nil {
		return err
	}

	if vp.Holder != holder {
		return fmt.Errorf("invalid auth response, invalid holder proof")
	}

	proofOfInterest := vp.Proofs[0]

	var proofChallenge, proofDomain string

	{
		d, ok := proofOfInterest["challenge"]
		if ok && d != nil {
			proofChallenge, ok = d.(string)
		}

		if !ok {
			return fmt.Errorf("invalid auth response proof, missing challenge")
		}
	}

	{
		d, ok := proofOfInterest["domain"]
		if ok && d != nil {
			proofDomain, ok = d.(string)
		}

		if !ok {
			return fmt.Errorf("invalid auth response proof, missing domain")
		}
	}

	if proofChallenge != challenge || proofDomain != domain {
		return fmt.Errorf("invalid proof and challenge in response")
	}

	return nil
}

func (c *Operation) storeCredential(cred []byte, vcsProfile string) error {
	storeVCBytes, err := prepareStoreVCRequest(cred, vcsProfile)
	if err != nil {
		return err
	}

	storeReq, err := http.NewRequest("POST", c.vcsURL+"/store", bytes.NewBuffer(storeVCBytes))
	if err != nil {
		return err
	}

	_, err = sendHTTPRequest(storeReq, c.httpClient, http.StatusOK, c.requestTokens[vcsIssuerRequestTokenName])
	if err != nil {
		return err
	}

	return nil
}

func (c *Operation) validateForm(formVals url.Values, keys ...string) error {
	for _, key := range keys {
		if _, found := getFormValue(key, formVals); !found {
			return fmt.Errorf("invalid '%s'", key)
		}
	}

	return nil
}

func prepareStoreVCRequest(cred []byte, profile string) ([]byte, error) {
	storeVCRequest := storeVC{
		Credential: string(cred),
		Profile:    profile,
	}

	return json.Marshal(storeVCRequest)
}

func prepareUpdateCredentialStatusRequest(vc *verifiable.Credential) ([]byte, error) {
	request := edgesvcops.UpdateCredentialStatusRequest{
		CredentialID:     vc.ID,
		CredentialStatus: edgesvcops.CredentialStatus{Type: csl.StatusList2021Entry, Status: "1"},
	}

	return json.Marshal(request)
}

func (c *Operation) getCMSData(tk *oauth2.Token, searchQuery, scope string) (string, map[string]interface{}, error) {
	userID, subjectBytes, err := c.getUserData(tk, searchQuery, scope)
	if err != nil {
		return "", nil, err
	}

	subjectMap, err := unmarshalSubject(subjectBytes)
	if err != nil {
		return "", nil, err
	}

	return userID, subjectMap, nil
}

func (c *Operation) getUserData(tk *oauth2.Token, searchQuery, scope string) (string, []byte, error) {
	user, err := c.getCMSUser(tk, searchQuery)
	if err != nil {
		return "", nil, err
	}

	// scope StudentCard matches studentcards in CMS etc.
	u := c.cmsURL + "/" + strings.ToLower(scope) + "s?userid=" + user.UserID

	httpClient := c.httpClient
	if tk != nil {
		httpClient = c.tokenIssuer.Client(tk)
	}

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", nil, err
	}

	respBytes, err := sendHTTPRequest(req, httpClient, http.StatusOK, "")
	if err != nil {
		return "", nil, err
	}

	return user.UserID, respBytes, nil
}

func signVCWithED25519(vc *verifiable.Credential, loader ld.DocumentLoader) error {
	edPriv := ed25519.PrivateKey(base58.Decode(pkBase58))
	edSigner := &edd25519Signer{edPriv}
	sigSuite := ed25519signature2018.New(suite.WithSigner(edSigner))

	tt := time.Now()

	ldpContext := &verifiable.LinkedDataProofContext{
		SignatureType:           "Ed25519Signature2018",
		SignatureRepresentation: verifiable.SignatureProofValue,
		Suite:                   sigSuite,
		VerificationMethod:      kid,
		Purpose:                 "assertionMethod",
		Created:                 &tt,
	}

	return vc.AddLinkedDataProof(ldpContext, jsonld.WithDocumentLoader(loader))
}

// nolint:interfacer
func sendHTTPRequest(req *http.Request, client *http.Client, status int, httpToken string) ([]byte, error) {
	if httpToken != "" {
		req.Header.Add("Authorization", "Bearer "+httpToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() {
		err = resp.Body.Close()
		if err != nil {
			logger.Warnf("failed to close response body")
		}
	}()

	if resp.StatusCode != status {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Warnf("failed to read response body for status: %d", resp.StatusCode)
		}

		return nil, fmt.Errorf("%s: %s", resp.Status, string(body))
	}

	return io.ReadAll(resp.Body)
}

// getFormValue reads form url value by key
func getFormValue(k string, vals url.Values) (string, bool) {
	if cr, ok := vals[k]; ok && len(cr) > 0 {
		return cr[0], true
	}

	return "", false
}

// writeResponse writes interface value to response
func (c *Operation) writeErrorResponse(rw http.ResponseWriter, status int, msg string) {
	logger.Errorf(msg)

	rw.WriteHeader(status)

	if _, err := rw.Write([]byte(msg)); err != nil {
		logger.Errorf("Unable to send error message, %s", err)
	}
}

// writeResponse writes interface value to response
func (c *Operation) writeResponse(rw http.ResponseWriter, status int, data []byte) {
	rw.WriteHeader(status)

	if _, err := rw.Write(data); err != nil {
		logger.Errorf("Unable to send error message, %s", err)
	}
}

// GetRESTHandlers get all controller API handler available for this service
func (c *Operation) GetRESTHandlers() []Handler {
	return c.handlers
}

func getTxnStore(prov storage.Provider) (storage.Store, error) {
	txnStore, err := prov.OpenStore(txnStoreName)
	if err != nil {
		return nil, err
	}

	return txnStore, nil
}

type storeVC struct {
	Credential string `json:"credential"`
	Profile    string `json:"profile,omitempty"`
}

type cmsUser struct {
	UserID string `json:"userid"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

type adapterDataReq struct {
	Token string `json:"token"`
}

type adapterTokenReq struct {
	State string `json:"state,omitempty"`
}

// IssuerTokenResp issuer user data token response.
type adapterTokenResp struct {
	Token  string `json:"token,omitempty"`
	UserID string `json:"userid"`
}

type userDataMap struct {
	ID             string          `json:"id,omitempty"`
	Data           json.RawMessage `json:"data,omitempty"`
	AssuranceScope string          `json:"assuranceScope,omitempty"`
}

func getCredStoreKeyPrefix(key string) string {
	return fmt.Sprintf("cred_store_%s", key)
}

func getAuthStateKeyPrefix(key string) string {
	return fmt.Sprintf("authstate_%s", key)
}

func getAuthCodeKeyPrefix(key string) string {
	return fmt.Sprintf("authcode_%s", key)
}

func getAccessTokenKeyPrefix(key string) string {
	return fmt.Sprintf("access_token_%s", key)
}

// signer for signing ed25519 for tests.
type edd25519Signer struct {
	privateKey []byte
}

func (s *edd25519Signer) Sign(doc []byte) ([]byte, error) {
	if l := len(s.privateKey); l != ed25519.PrivateKeySize {
		return nil, errors.New("ed25519: bad private key length")
	}

	return ed25519.Sign(s.privateKey, doc), nil
}

func (s *edd25519Signer) Alg() string {
	return ""
}

func (c *Operation) prepareAuthCodeURL(w http.ResponseWriter, scope string) string {
	u := c.tokenIssuer.AuthCodeURL(w)
	if scope == externalScopeQueryParam {
		u += "&scope=" + "PermanentResidentCard"
	} else {
		u += "&scope=" + scope
	}

	return u
}
