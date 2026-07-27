//go:build all || unit

package hiero

// SPDX-License-Identifier: Apache-2.0

import (
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	protobuf "google.golang.org/protobuf/proto"
)

func TestUnitMirrorNodeAccountBalanceQueryGetSet(t *testing.T) {
	t.Parallel()

	accountID := AccountID{Account: 1234}
	contractID := ContractID{Contract: 5678}

	q := NewMirrorNodeAccountBalanceQuery().
		SetAccountID(accountID).
		SetMaxAttempts(5)

	assert.Equal(t, accountID, q.GetAccountID())
	assert.Equal(t, uint64(5), q.GetMaxAttempts())

	// Setting a contract ID clears the account ID (they are mutually exclusive).
	q.SetContractID(contractID)
	assert.Equal(t, contractID, q.GetContractID())
	assert.Equal(t, AccountID{}, q.GetAccountID())
}

func TestUnitMirrorNodeAccountPathID(t *testing.T) {
	t.Parallel()

	// Plain account number.
	assert.Equal(t, "0.0.1234", mirrorNodeAccountPathID(AccountID{Account: 1234}))

	// EVM-address alias: bare hex, no shard.realm prefix.
	evm := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99,
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x01, 0x23, 0x45, 0x67}
	assert.Equal(t, "00112233445566778899aabbccddeeff01234567",
		mirrorNodeAccountPathID(AccountID{AliasEvmAddress: &evm}))

	// Public-key alias: base32 (no padding) of the protobuf-serialized key, with no shard.realm
	// prefix -- the representation the mirror node accepts and reports.
	key, err := PrivateKeyGenerateEd25519()
	require.NoError(t, err)
	aliasID := *key.PublicKey().ToAccountID(0, 0)

	path := mirrorNodeAccountPathID(aliasID)
	assert.NotContains(t, path, "0.0.", "public-key alias must not carry a shard.realm prefix")

	aliasBytes, err := protobuf.Marshal(aliasID.AliasKey._ToProtoKey())
	require.NoError(t, err)
	expected := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(aliasBytes)
	assert.Equal(t, expected, path)

	// The encoding must decode back to the original alias bytes.
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(path)
	require.NoError(t, err)
	assert.Equal(t, aliasBytes, decoded)
}

func TestUnitMirrorNodeAccountBalanceQueryNilClient(t *testing.T) {
	t.Parallel()

	_, err := NewMirrorNodeAccountBalanceQuery().
		SetAccountID(AccountID{Account: 1234}).
		Execute(nil)
	require.ErrorIs(t, err, errNoClientProvided)
}

func TestUnitMirrorNodeAccountBalanceQueryNoIDSet(t *testing.T) {
	t.Parallel()

	client, err := _NewMockClient()
	require.NoError(t, err)
	client.SetLedgerID(*NewLedgerIDTestnet())
	client.SetMirrorNetwork([]string{"mirror.example.com:443"})

	_, err = NewMirrorNodeAccountBalanceQuery().Execute(client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "either an account ID or a contract ID must be set")
}

func TestUnitMirrorNodeAccountBalanceQuerySuccess(t *testing.T) {
	// Not parallel: SetupMockTransportForDomain mutates http.DefaultTransport.
	const domain = "balance.example.com:443"

	var accountPath, tokensPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/tokens") {
			tokensPath = r.URL.Path
			require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
				"tokens": []map[string]interface{}{
					{"token_id": "0.0.1001", "balance": 42, "decimals": 2},
					{"token_id": "0.0.1002", "balance": 7, "decimals": 0},
				},
				"links": map[string]interface{}{"next": nil},
			}))
			return
		}
		accountPath = r.URL.Path
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
			"account": "0.0.1234",
			"balance": map[string]interface{}{
				"balance": 500000000,
				// The embedded token list is truncated by the mirror node and must be ignored;
				// return a bogus entry to prove it is not used.
				"tokens": []map[string]interface{}{
					{"token_id": "0.0.9999", "balance": 999},
				},
			},
		}))
	}))
	defer server.Close()

	cleanup := SetupMockTransportForDomain(domain, server.URL)
	defer cleanup()

	client, err := _NewMockClient()
	require.NoError(t, err)
	client.SetLedgerID(*NewLedgerIDTestnet())
	client.SetMirrorNetwork([]string{domain})

	balance, err := NewMirrorNodeAccountBalanceQuery().
		SetAccountID(AccountID{Account: 1234}).
		Execute(client)
	require.NoError(t, err)

	assert.True(t, strings.HasSuffix(accountPath, "/accounts/0.0.1234"), "unexpected account path: %s", accountPath)
	assert.True(t, strings.HasSuffix(tokensPath, "/accounts/0.0.1234/tokens"), "unexpected tokens path: %s", tokensPath)

	assert.Equal(t, HbarFromTinybar(500000000).AsTinybar(), balance.Hbars.AsTinybar())

	// Token balances come from the /tokens endpoint, not the truncated embedded list.
	assert.Equal(t, uint64(42), balance.Tokens.Get(TokenID{Token: 1001}))
	assert.Equal(t, uint64(7), balance.Tokens.Get(TokenID{Token: 1002}))
	assert.Equal(t, uint64(42), balance.Token[TokenID{Token: 1001}])
	assert.Equal(t, uint64(0), balance.Tokens.Get(TokenID{Token: 9999}))

	// Decimals are populated from the /tokens endpoint.
	assert.Equal(t, uint64(2), balance.TokenDecimals.Get(TokenID{Token: 1001}))
	assert.Equal(t, uint64(0), balance.TokenDecimals.Get(TokenID{Token: 1002}))
}

func TestUnitMirrorNodeAccountBalanceQueryPagination(t *testing.T) {
	// Not parallel: SetupMockTransportForDomain mutates http.DefaultTransport.
	const domain = "balancepaged.example.com:443"

	tokensCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/tokens") {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
				"balance": map[string]interface{}{"balance": 100},
			}))
			return
		}

		// Second page is requested once the first page hands back a links.next.
		if r.URL.Query().Get("page") == "2" {
			tokensCalls++
			require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
				"tokens": []map[string]interface{}{
					{"token_id": "0.0.2002", "balance": 20, "decimals": 4},
				},
				"links": map[string]interface{}{"next": nil},
			}))
			return
		}

		tokensCalls++
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
			"tokens": []map[string]interface{}{
				{"token_id": "0.0.2001", "balance": 10, "decimals": 1},
			},
			"links": map[string]interface{}{
				"next": "/api/v1/accounts/0.0.1234/tokens?page=2",
			},
		}))
	}))
	defer server.Close()

	cleanup := SetupMockTransportForDomain(domain, server.URL)
	defer cleanup()

	client, err := _NewMockClient()
	require.NoError(t, err)
	client.SetLedgerID(*NewLedgerIDTestnet())
	client.SetMirrorNetwork([]string{domain})

	balance, err := NewMirrorNodeAccountBalanceQuery().
		SetAccountID(AccountID{Account: 1234}).
		Execute(client)
	require.NoError(t, err)

	assert.Equal(t, 2, tokensCalls, "expected both token pages to be fetched")
	assert.Equal(t, uint64(10), balance.Tokens.Get(TokenID{Token: 2001}))
	assert.Equal(t, uint64(20), balance.Tokens.Get(TokenID{Token: 2002}))
	assert.Equal(t, uint64(1), balance.TokenDecimals.Get(TokenID{Token: 2001}))
	assert.Equal(t, uint64(4), balance.TokenDecimals.Get(TokenID{Token: 2002}))
}

func TestUnitMirrorNodeAccountBalanceQueryNon200(t *testing.T) {
	// Not parallel: SetupMockTransportForDomain mutates http.DefaultTransport.
	const domain = "balancenotfound.example.com:443"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"_status":{"messages":[{"message":"Not found"}]}}`))
	}))
	defer server.Close()

	cleanup := SetupMockTransportForDomain(domain, server.URL)
	defer cleanup()

	client, err := _NewMockClient()
	require.NoError(t, err)
	client.SetLedgerID(*NewLedgerIDTestnet())
	client.SetMirrorNetwork([]string{domain})

	_, err = NewMirrorNodeAccountBalanceQuery().
		SetAccountID(AccountID{Account: 9999}).
		Execute(client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-200")
}

func TestUnitMirrorNodeAccountBalanceQueryTokensNon200(t *testing.T) {
	// Not parallel: SetupMockTransportForDomain mutates http.DefaultTransport.
	const domain = "balancetokenserr.example.com:443"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tokens") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"_status":{"messages":[{"message":"Not found"}]}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
			"balance": map[string]interface{}{"balance": 100},
		}))
	}))
	defer server.Close()

	cleanup := SetupMockTransportForDomain(domain, server.URL)
	defer cleanup()

	client, err := _NewMockClient()
	require.NoError(t, err)
	client.SetLedgerID(*NewLedgerIDTestnet())
	client.SetMirrorNetwork([]string{domain})

	_, err = NewMirrorNodeAccountBalanceQuery().
		SetAccountID(AccountID{Account: 1234}).
		Execute(client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-200")
}

func TestUnitMirrorNodeAccountBalanceQueryContractUsesAccountsEndpoint(t *testing.T) {
	// Not parallel: SetupMockTransportForDomain mutates http.DefaultTransport.
	const domain = "balancecontract.example.com:443"

	var accountPath, tokensPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/tokens") {
			tokensPath = r.URL.Path
			require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
				"tokens": []map[string]interface{}{},
				"links":  map[string]interface{}{"next": nil},
			}))
			return
		}
		accountPath = r.URL.Path
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
			"balance": map[string]interface{}{"balance": 123},
		}))
	}))
	defer server.Close()

	cleanup := SetupMockTransportForDomain(domain, server.URL)
	defer cleanup()

	client, err := _NewMockClient()
	require.NoError(t, err)
	client.SetLedgerID(*NewLedgerIDTestnet())
	client.SetMirrorNetwork([]string{domain})

	balance, err := NewMirrorNodeAccountBalanceQuery().
		SetContractID(ContractID{Contract: 5678}).
		Execute(client)
	require.NoError(t, err)

	// Contracts are routed through the accounts endpoint, not the contracts endpoint.
	assert.True(t, strings.HasSuffix(accountPath, "/accounts/0.0.5678"), "unexpected account path: %s", accountPath)
	assert.True(t, strings.HasSuffix(tokensPath, "/accounts/0.0.5678/tokens"), "unexpected tokens path: %s", tokensPath)
	assert.Equal(t, HbarFromTinybar(123).AsTinybar(), balance.Hbars.AsTinybar())
}
