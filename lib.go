package cosmwasm

import (
	"encoding/json"
	"fmt"

	"github.com/evoblockchain/wasmvm/internal/api"
	"github.com/evoblockchain/wasmvm/types"
)

// Checksum represents a hash of the Wasm bytecode that serves as an ID. Must be generated from this library.
type Checksum []byte

// WasmCode is an alias for raw bytes of the wasm compiled code
type WasmCode []byte

// KVStore is a reference to some sub-kvstore that is valid for one instance of a code
type KVStore = api.KVStore

// GoAPI is a reference to some "precompiles", go callbacks
type GoAPI = api.GoAPI

// Querier lets us make read-only queries on other modules
type Querier = types.Querier

// GasMeter is a read-only version of the sdk gas meter
type GasMeter = api.GasMeter

// VM is the main entry point to this library.
// You should create an instance with its own subdirectory to manage state inside,
// and call it for all cosmwasm code related actions.
type VM struct {
	cache      api.Cache
	printDebug bool
}

// NewVM creates a new VM.
//
// `dataDir` is a base directory for Wasm blobs and various caches.
// `supportedFeatures` is a comma separated list of features suppored by the chain.
// `memoryLimit` is the memory limit of each contract execution (in MiB)
// `printDebug` is a flag to enable/disable printing debug logs from the contract to STDOUT. This should be false in production environments.
// `cacheSize` sets the size in MiB of an in-memory cache for e.g. module caching. Set to 0 to disable.
// `deserCost` sets the gas cost of deserializing one byte of data.
func NewVM(dataDir string, supportedFeatures string, memoryLimit uint32, printDebug bool, cacheSize uint32) (*VM, error) {
	cache, err := api.InitCache(dataDir, supportedFeatures, cacheSize, memoryLimit)
	if err != nil {
		return nil, err
	}
	return &VM{cache: cache, printDebug: printDebug}, nil
}

// Cleanup should be called when no longer using this to free resources on the rust-side
func (vm *VM) Cleanup() {
	api.ReleaseCache(vm.cache)
}

// Create will compile the wasm code, and store the resulting pre-compile
// as well as the original code. Both can be referenced later via Checksum
// This must be done one time for given code, after which it can be
// instatitated many times, and each instance called many times.
//
// For example, the code for all ERC-20 contracts should be the same.
// This function stores the code for that contract only once, but it can
// be instantiated with custom inputs in the future.
//
// TODO: return gas cost? Add gas limit??? there is no metering here...
func (vm *VM) Create(code WasmCode) (Checksum, error) {
	return api.Create(vm.cache, code)
}

// GetCode will load the original wasm code for the given code id.
// This will only succeed if that code id was previously returned from
// a call to Create.
//
// This can be used so that the (short) code id (hash) is stored in the iavl tree
// and the larger binary blobs (wasm and pre-compiles) are all managed by the
// rust library
func (vm *VM) GetCode(checksum Checksum) (WasmCode, error) {
	return api.GetCode(vm.cache, checksum)
}

// Pin pins a code to an in-memory cache, such that is
// always loaded quickly when executed.
// Pin is idempotent.
func (vm *VM) Pin(checksum Checksum) error {
	return api.Pin(vm.cache, checksum)
}

// Unpin removes the guarantee of a contract to be pinned (see Pin).
// After calling this, the code may or may not remain in memory depending on
// the implementor's choice.
// Unpin is idempotent.
func (vm *VM) Unpin(checksum Checksum) error {
	return api.Unpin(vm.cache, checksum)
}

// Returns a report of static analysis of the wasm contract (uncompiled).
// This contract must have been stored in the cache previously (via Create).
// Only info currently returned is if it exposes all ibc entry points, but this may grow later
func (vm *VM) AnalyzeCode(checksum Checksum) (*types.AnalysisReport, error) {
	return api.AnalyzeCode(vm.cache, checksum)
}

// GetMetrics some internal metrics for monitoring purposes.
func (vm *VM) GetMetrics() (*types.Metrics, error) {
	return api.GetMetrics(vm.cache)
}

// Instantiate will create a new contract based on the given Checksum.
// We can set the initMsg (contract "genesis") here, and it then receives
// an account and address and can be invoked (Execute) many times.
//
// Storage should be set with a PrefixedKVStore that this code can safely access.
//
// Under the hood, we may recompile the wasm, use a cached native compile, or even use a cached instance
// for performance.
func (vm *VM) Instantiate(
	checksum Checksum,
	env types.Env,
	info types.MessageInfo,
	initMsg []byte,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.Response, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	infoBin, err := json.Marshal(info)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.Instantiate(vm.cache, checksum, envBin, infoBin, initMsg, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var result types.ContractResult
	err = json.Unmarshal(data, &result)
	if err != nil {
		return nil, gasUsed, err
	}
	if result.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", result.Err)
	}
	return result.Ok, gasUsed, nil
}

// Execute calls a given contract. Since the only difference between contracts with the same Checksum is the
// data in their local storage, and their address in the outside world, we need no ContractID here.
// (That is a detail for the external, sdk-facing, side).
//
// The caller is responsible for passing the correct `store` (which must have been initialized exactly once),
// and setting the env with relevent info on this instance (address, balance, etc)
func (vm *VM) Execute(
	checksum Checksum,
	env types.Env,
	info types.MessageInfo,
	executeMsg []byte,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.Response, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	infoBin, err := json.Marshal(info)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.Execute(vm.cache, checksum, envBin, infoBin, executeMsg, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}

	gasUsed += gasForDeserialization
	var result types.ContractResult
	err = json.Unmarshal(data, &result)
	if err != nil {
		return nil, gasUsed, err
	}
	if result.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", result.Err)
	}
	return result.Ok, gasUsed, nil
}

// Query allows a client to execute a contract-specific query. If the result is not empty, it should be
// valid json-encoded data to return to the client.
// The meaning of path and data can be determined by the code. Path is the suffix of the abci.QueryRequest.Path
func (vm *VM) Query(
	checksum Checksum,
	env types.Env,
	queryMsg []byte,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) ([]byte, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.Query(vm.cache, checksum, envBin, queryMsg, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.QueryResponse
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// Migrate will migrate an existing contract to a new code binary.
// This takes storage of the data from the original contract and the Checksum of the new contract that should
// replace it. This allows it to run a migration step if needed, or return an error if unable to migrate
// the given data.
//
// MigrateMsg has some data on how to perform the migration.
func (vm *VM) Migrate(
	checksum Checksum,
	env types.Env,
	migrateMsg []byte,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.Response, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.Migrate(vm.cache, checksum, envBin, migrateMsg, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.ContractResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// Sudo allows native Go modules to make priviledged (sudo) calls on the contract.
// The contract can expose entry points that cannot be triggered by any transaction, but only via
// native Go modules, and delegate the access control to the system.
//
// These work much like Migrate (same scenario) but allows custom apps to extend the priviledged entry points
// without forking cosmwasm-vm.
func (vm *VM) Sudo(
	checksum Checksum,
	env types.Env,
	sudoMsg []byte,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.Response, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.Sudo(vm.cache, checksum, envBin, sudoMsg, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.ContractResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// Reply allows the native Go wasm modules to make a priviledged call to return the result
// of executing a SubMsg.
//
// These work much like Sudo (same scenario) but focuses on one specific case (and one message type)
func (vm *VM) Reply(
	checksum Checksum,
	env types.Env,
	reply types.Reply,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.Response, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	replyBin, err := json.Marshal(reply)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.Reply(vm.cache, checksum, envBin, replyBin, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.ContractResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// IBCChannelOpen is available on IBC-enabled contracts and is a hook to call into
// during the handshake pahse
func (vm *VM) IBCChannelOpen(
	checksum Checksum,
	env types.Env,
	msg types.IBCChannelOpenMsg,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.IBC3ChannelOpenResponse, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	msgBin, err := json.Marshal(msg)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.IBCChannelOpen(vm.cache, checksum, envBin, msgBin, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.IBCChannelOpenResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// IBCChannelConnect is available on IBC-enabled contracts and is a hook to call into
// during the handshake pahse
func (vm *VM) IBCChannelConnect(
	checksum Checksum,
	env types.Env,
	msg types.IBCChannelConnectMsg,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.IBCBasicResponse, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	msgBin, err := json.Marshal(msg)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.IBCChannelConnect(vm.cache, checksum, envBin, msgBin, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.IBCBasicResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// IBCChannelClose is available on IBC-enabled contracts and is a hook to call into
// at the end of the channel lifetime
func (vm *VM) IBCChannelClose(
	checksum Checksum,
	env types.Env,
	msg types.IBCChannelCloseMsg,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.IBCBasicResponse, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	msgBin, err := json.Marshal(msg)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.IBCChannelClose(vm.cache, checksum, envBin, msgBin, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.IBCBasicResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// IBCPacketReceive is available on IBC-enabled contracts and is called when an incoming
// packet is received on a channel belonging to this contract
func (vm *VM) IBCPacketReceive(
	checksum Checksum,
	env types.Env,
	msg types.IBCPacketReceiveMsg,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.IBCReceiveResult, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	msgBin, err := json.Marshal(msg)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.IBCPacketReceive(vm.cache, checksum, envBin, msgBin, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.IBCReceiveResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	return &resp, gasUsed, nil
}

// IBCPacketAck is available on IBC-enabled contracts and is called when an
// the response for an outgoing packet (previously sent by this contract)
// is received
func (vm *VM) IBCPacketAck(
	checksum Checksum,
	env types.Env,
	msg types.IBCPacketAckMsg,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.IBCBasicResponse, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	msgBin, err := json.Marshal(msg)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.IBCPacketAck(vm.cache, checksum, envBin, msgBin, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.IBCBasicResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// IBCPacketTimeout is available on IBC-enabled contracts and is called when an
// outgoing packet (previously sent by this contract) will provably never be executed.
// Usually handled like ack returning an error
func (vm *VM) IBCPacketTimeout(
	checksum Checksum,
	env types.Env,
	msg types.IBCPacketTimeoutMsg,
	store KVStore,
	goapi GoAPI,
	querier Querier,
	gasMeter GasMeter,
	gasLimit uint64,
	deserCost types.UFraction,
) (*types.IBCBasicResponse, uint64, error) {
	envBin, err := json.Marshal(env)
	if err != nil {
		return nil, 0, err
	}
	msgBin, err := json.Marshal(msg)
	if err != nil {
		return nil, 0, err
	}
	data, gasUsed, err := api.IBCPacketTimeout(vm.cache, checksum, envBin, msgBin, &gasMeter, store, &goapi, &querier, gasLimit, vm.printDebug)
	if err != nil {
		return nil, gasUsed, err
	}

	gasForDeserialization := deserCost.Mul(uint64(len(data))).Floor()
	if gasLimit < gasForDeserialization+gasUsed {
		return nil, gasUsed, fmt.Errorf("Insufficient gas left to deserialize contract execution result (%d bytes)", len(data))
	}
	gasUsed += gasForDeserialization

	var resp types.IBCBasicResult
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, gasUsed, err
	}
	if resp.Err != "" {
		return nil, gasUsed, fmt.Errorf("%s", resp.Err)
	}
	return resp.Ok, gasUsed, nil
}

// LibwasmvmVersion returns the version of the loaded library
// at runtime. This can be used for debugging to verify the loaded version
// matches the expected version.
func LibwasmvmVersion() (string, error) {
	return api.LibwasmvmVersion()
}
