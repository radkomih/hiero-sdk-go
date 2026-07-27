//go:build all || unit

package hiero

// SPDX-License-Identifier: Apache-2.0

import (
	"testing"
	"time"

	"github.com/hiero-ledger/hiero-sdk-go/v2/proto/services"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
)

func TestUnitAccountBalanceQueryValidate(t *testing.T) {
	t.Parallel()

	client, err := _NewMockClient()
	client.SetLedgerID(*NewLedgerIDTestnet())
	require.NoError(t, err)
	client.SetAutoValidateChecksums(true)
	accountID, err := AccountIDFromString("0.0.123-esxsf")
	require.NoError(t, err)

	balanceQuery := NewAccountBalanceQuery().
		SetAccountID(accountID)

	err = balanceQuery.validateNetworkOnIDs(client)
	require.NoError(t, err)
}

func TestUnitAccountBalanceQueryValidateWrong(t *testing.T) {
	t.Parallel()

	client, err := _NewMockClient()
	client.SetLedgerID(*NewLedgerIDTestnet())
	require.NoError(t, err)
	client.SetAutoValidateChecksums(true)
	accountID, err := AccountIDFromString("0.0.123-rmkykd")
	require.NoError(t, err)

	balanceQuery := NewAccountBalanceQuery().
		SetAccountID(accountID)

	err = balanceQuery.validateNetworkOnIDs(client)
	assert.Error(t, err)
	if err != nil {
		assert.Equal(t, "network mismatch or wrong checksum given, given checksum: rmkykd, correct checksum esxsf, network: testnet", err.Error())
	}
}

func TestUnitAccountBalanceQueryGet(t *testing.T) {
	t.Parallel()

	spenderAccountID1 := AccountID{Account: 7}

	balance := NewAccountBalanceQuery().
		SetAccountID(spenderAccountID1).
		SetNodeAccountIDs([]AccountID{{Account: 10}, {Account: 11}, {Account: 12}})

	balance.GetAccountID()
	balance.GetNodeAccountIDs()
	balance.GetPaymentTransactionID()
}

func TestUnitAccountBalanceQuerySetNothing(t *testing.T) {
	t.Parallel()

	balance := NewAccountBalanceQuery()

	balance.GetAccountID()
	balance.GetNodeAccountIDs()
	balance.GetPaymentTransactionID()
}

func TestUnitAccountBalanceQueryCoverage(t *testing.T) {
	t.Parallel()

	checksum := "dmqui"
	contract := ContractID{Contract: 3, checksum: &checksum}
	account := AccountID{Account: 3, checksum: &checksum}
	nodeAccountID := []AccountID{{Account: 10}}
	transactionID := TransactionIDGenerate(AccountID{Account: 324})

	client, err := _NewMockClient()
	client.SetLedgerID(*NewLedgerIDTestnet())
	require.NoError(t, err)
	client.SetAutoValidateChecksums(true)

	query := NewAccountBalanceQuery().
		SetMaxRetry(3).
		SetMaxBackoff(time.Second * 30).
		SetMinBackoff(time.Second * 10).
		SetAccountID(account).
		SetContractID(contract).
		SetNodeAccountIDs(nodeAccountID).
		SetPaymentTransactionID(transactionID).
		SetMaxQueryPayment(NewHbar(23)).
		SetQueryPayment(NewHbar(3))

	err = query.validateNetworkOnIDs(client)

	require.NoError(t, err)
	query.GetNodeAccountIDs()
	query.GetMaxBackoff()
	query.GetMinBackoff()
	query.getName()
	query.GetAccountID()
	query.GetContractID()

	_AccountBalanceFromProtobuf(nil)
	bal := AccountBalance{Hbars: NewHbar(2)}
	bal._ToProtobuf()
}

// TestUnitAccountBalanceQueryDeprecatedExecute verifies that Execute returns the deprecation error
// immediately without any consensus-node call; the mock fails the test on any received request.
func TestUnitAccountBalanceQueryDeprecatedExecute(t *testing.T) {
	t.Parallel()

	call := func(request *services.Query) *services.Response {
		t.Error("AccountBalanceQuery.Execute must not make any consensus node calls")
		return &services.Response{}
	}

	responses := [][]interface{}{{call}}

	client, server := NewMockClientAndServer(responses)
	defer server.Close()

	_, err := NewAccountBalanceQuery().
		SetNodeAccountIDs([]AccountID{{Account: 3}}).
		SetAccountID(AccountID{Account: 1800}).
		Execute(client)

	require.ErrorIs(t, err, errAccountBalanceQueryDeprecated)
	require.EqualError(t, err, "Deprecated: AccountBalanceQuery is no longer supported. Use MirrorNodeAccountBalanceQuery or the mirror node REST API (GET /api/v1/accounts/{id}) to retrieve account balances.")
}

// TestUnitAccountBalanceQueryDeprecatedGetCost verifies that GetCost also returns the deprecation
// error without contacting the network.
func TestUnitAccountBalanceQueryDeprecatedGetCost(t *testing.T) {
	t.Parallel()

	_, err := NewAccountBalanceQuery().
		SetAccountID(AccountID{Account: 1800}).
		GetCost(nil)

	require.ErrorIs(t, err, errAccountBalanceQueryDeprecated)
}

func TestUnitAccountBalanceQueryNoClient(t *testing.T) {
	t.Parallel()

	// Execute now returns the deprecation error before any client/network validation.
	_, err := NewAccountBalanceQuery().
		Execute(nil)

	require.ErrorIs(t, err, errAccountBalanceQueryDeprecated)
}
