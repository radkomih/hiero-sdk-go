package hiero

// SPDX-License-Identifier: Apache-2.0

import (
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/pkg/errors"
	protobuf "google.golang.org/protobuf/proto"
)

// mirrorNodeAccountBalanceMaxPages caps how many pages of the /tokens endpoint are followed so a
// misbehaving mirror node cannot loop forever.
const mirrorNodeAccountBalanceMaxPages = 100

// MirrorNodeAccountBalanceQuery retrieves the hbar and token balances of an account (or contract)
// from the mirror node REST API. It is the recommended replacement for the deprecated
// AccountBalanceQuery.
//
// The hbar balance is read from GET /api/v1/accounts/{id}. Token balances and decimals are read
// from the paginated GET /api/v1/accounts/{id}/tokens endpoint (the token list embedded in the
// accounts response is truncated and is not used).
type MirrorNodeAccountBalanceQuery struct {
	accountID   *AccountID
	contractID  *ContractID
	timeout     time.Duration
	maxAttempts uint64
}

// mirrorNodeAccountBalanceResponse models the subset of the GET /api/v1/accounts/{id} response
// that describes the account's hbar balance.
type mirrorNodeAccountBalanceResponse struct {
	Balance struct {
		Balance int64 `json:"balance"`
	} `json:"balance"`
}

// mirrorNodeAccountTokensResponse models a single page of the GET /api/v1/accounts/{id}/tokens
// response.
type mirrorNodeAccountTokensResponse struct {
	Tokens []struct {
		TokenID  string `json:"token_id"`
		Balance  uint64 `json:"balance"`
		Decimals uint64 `json:"decimals"`
	} `json:"tokens"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
}

// NewMirrorNodeAccountBalanceQuery creates a MirrorNodeAccountBalanceQuery which can be used to
// retrieve the balance of an account or contract from the mirror node REST API.
func NewMirrorNodeAccountBalanceQuery() *MirrorNodeAccountBalanceQuery {
	return &MirrorNodeAccountBalanceQuery{
		timeout:     mirrorNodeDefaultTimeout,
		maxAttempts: mirrorNodeDefaultMaxAttempts,
	}
}

// SetAccountID sets the AccountID for which you wish to query the balance.
//
// Note: you can only query an Account or Contract but not both -- if a Contract ID or Account ID
// has already been set, it will be overwritten by this method.
func (q *MirrorNodeAccountBalanceQuery) SetAccountID(accountID AccountID) *MirrorNodeAccountBalanceQuery {
	q.accountID = &accountID
	q.contractID = nil
	return q
}

// GetAccountID returns the AccountID for which you wish to query the balance.
func (q *MirrorNodeAccountBalanceQuery) GetAccountID() AccountID {
	if q.accountID == nil {
		return AccountID{}
	}
	return *q.accountID
}

// SetContractID sets the ContractID for which you wish to query the balance.
//
// Note: you can only query an Account or Contract but not both -- if a Contract ID or Account ID
// has already been set, it will be overwritten by this method.
func (q *MirrorNodeAccountBalanceQuery) SetContractID(contractID ContractID) *MirrorNodeAccountBalanceQuery {
	q.contractID = &contractID
	q.accountID = nil
	return q
}

// GetContractID returns the ContractID for which you wish to query the balance.
func (q *MirrorNodeAccountBalanceQuery) GetContractID() ContractID {
	if q.contractID == nil {
		return ContractID{}
	}
	return *q.contractID
}

// SetTimeout sets the per-request timeout for the mirror node REST call. A timeout of 0 disables it.
func (q *MirrorNodeAccountBalanceQuery) SetTimeout(timeout time.Duration) *MirrorNodeAccountBalanceQuery {
	q.timeout = timeout
	return q
}

// GetTimeout returns the per-request timeout for the mirror node REST call.
func (q *MirrorNodeAccountBalanceQuery) GetTimeout() time.Duration {
	return q.timeout
}

// SetMaxAttempts sets how many times a transient (transport or 5xx/429) failure is retried.
func (q *MirrorNodeAccountBalanceQuery) SetMaxAttempts(maxAttempts uint64) *MirrorNodeAccountBalanceQuery {
	q.maxAttempts = maxAttempts
	return q
}

// GetMaxAttempts returns how many times a transient failure is retried.
func (q *MirrorNodeAccountBalanceQuery) GetMaxAttempts() uint64 {
	return q.maxAttempts
}

// Execute queries the mirror node REST API and returns the balance of the configured account or
// contract, including token balances and decimals.
func (q *MirrorNodeAccountBalanceQuery) Execute(client *Client) (AccountBalance, error) {
	if client == nil {
		return AccountBalance{}, errNoClientProvided
	}
	if client.mirrorNetwork == nil || len(client.GetMirrorNetwork()) == 0 {
		return AccountBalance{}, errors.New("mirror node is not set")
	}

	var idStr string
	switch {
	case q.accountID != nil:
		idStr = mirrorNodeAccountPathID(*q.accountID)
	case q.contractID != nil:
		idStr = q.contractID.String()
	default:
		return AccountBalance{}, errors.New("either an account ID or a contract ID must be set")
	}

	baseURL, err := client.GetMirrorRestApiBaseUrl()
	if err != nil {
		return AccountBalance{}, err
	}

	hbars, err := q.fetchHbarBalance(client, baseURL, idStr)
	if err != nil {
		return AccountBalance{}, err
	}

	balances, decimals, err := q.fetchTokenBalances(client, baseURL, idStr)
	if err != nil {
		return AccountBalance{}, err
	}

	tokenMap := make(map[TokenID]uint64, len(balances))
	for tokenStr, balance := range balances {
		if tokenID, err := TokenIDFromString(tokenStr); err == nil {
			tokenMap[tokenID] = balance
		}
	}

	return AccountBalance{
		Hbars:         hbars,
		Token:         tokenMap,
		Tokens:        TokenBalanceMap{balances: balances},
		TokenDecimals: TokenDecimalMap{decimals: decimals},
	}, nil
}

// mirrorNodeAccountPathID renders an AccountID for use in a mirror node REST path, matching the
// formats the /accounts/{idOrAliasOrEvmAddress} endpoint accepts:
//   - an EVM-address alias as bare hex (as AccountID._MirrorNodeRequest already does);
//   - a public-key alias as the base32 (RFC 4648, no padding) encoding of the protobuf-serialized
//     key -- the same alias representation the mirror node reports;
//   - everything else as the standard shard.realm.num string.
func mirrorNodeAccountPathID(id AccountID) string {
	switch {
	case id.AliasEvmAddress != nil:
		return hex.EncodeToString(*id.AliasEvmAddress)
	case id.AliasKey != nil:
		if aliasBytes, err := protobuf.Marshal(id.AliasKey._ToProtoKey()); err == nil {
			return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(aliasBytes)
		}
		return id.String()
	default:
		return id.String()
	}
}

// fetchHbarBalance retrieves the hbar balance from GET /api/v1/accounts/{id}.
func (q *MirrorNodeAccountBalanceQuery) fetchHbarBalance(client *Client, baseURL, idStr string) (Hbar, error) {
	url := fmt.Sprintf("%s/accounts/%s", baseURL, idStr)

	body, err := q.getPage(client, url)
	if err != nil {
		return Hbar{}, err
	}

	var parsed mirrorNodeAccountBalanceResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Hbar{}, fmt.Errorf("failed to decode mirror node response: %w", err)
	}

	return HbarFromTinybar(parsed.Balance.Balance), nil
}

// fetchTokenBalances retrieves all token balances and decimals from the paginated
// GET /api/v1/accounts/{id}/tokens endpoint, following links.next until it is absent.
func (q *MirrorNodeAccountBalanceQuery) fetchTokenBalances(client *Client, baseURL, idStr string) (map[string]uint64, map[string]uint64, error) {
	balances := make(map[string]uint64)
	decimals := make(map[string]uint64)

	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid mirror REST API base URL %q: %w", baseURL, err)
	}

	pageURL := fmt.Sprintf("%s/accounts/%s/tokens", baseURL, idStr)

	for range mirrorNodeAccountBalanceMaxPages {
		body, err := q.getPage(client, pageURL)
		if err != nil {
			return nil, nil, err
		}

		var parsed mirrorNodeAccountTokensResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, nil, fmt.Errorf("failed to decode mirror node response: %w", err)
		}

		for _, token := range parsed.Tokens {
			tokenID, err := TokenIDFromString(token.TokenID)
			if err != nil {
				continue
			}
			balances[tokenID.String()] = token.Balance
			decimals[tokenID.String()] = token.Decimals
		}

		if parsed.Links.Next == nil || *parsed.Links.Next == "" {
			return balances, decimals, nil
		}

		next, err := resolveNextURL(base, *parsed.Links.Next)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid pagination next link %q: %w", *parsed.Links.Next, err)
		}
		pageURL = next
	}

	return nil, nil, fmt.Errorf("exceeded pagination cap of %d pages", mirrorNodeAccountBalanceMaxPages)
}

// getPage issues a single retrying GET and returns the response body, translating transport
// failures and non-200 responses into errors.
func (q *MirrorNodeAccountBalanceQuery) getPage(client *Client, url string) ([]byte, error) {
	resp, err := mirrorNodeGetWithRetry(client, url, q.maxAttempts, q.timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to send request to mirror node: %w", err)
	}
	if resp == nil {
		return nil, errors.New("received nil response from mirror node")
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received non-200 response from mirror node: %d, details: %s", resp.StatusCode, body)
	}
	if readErr != nil {
		return nil, fmt.Errorf("failed to read mirror node response: %w", readErr)
	}

	return body, nil
}
