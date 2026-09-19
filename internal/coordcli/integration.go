package coordcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const maxIntegrationParamsBytes = 32 << 10

// integrationCommand is the deliberately closed agent integration surface.
// The socket supplies the caller identity; params files never carry an actor,
// run owner, approval, or other transport identity.
func integrationCommand(ctx context.Context, socket, name string, args []string, in io.Reader) (any, error) {
	if name == "" {
		return nil, usageError("integration requires a subcommand")
	}
	if name != "prepare" && name != "show" && name != "verify" && name != "request-delivery" && name != "deliver" {
		return nil, usageError("unknown integration command: " + name)
	}
	paramsFile, err := parseIntegrationFlags(name, args)
	if err != nil {
		return nil, err
	}

	switch name {
	case "prepare":
		var params protocol.IntegrationPrepareParams
		if err := readIntegrationParams(paramsFile, in, &params); err != nil {
			return nil, err
		}
		var result protocol.IntegrationPrepareResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodIntegrationPrepare, params, &result); err != nil {
			return result, err
		}
		return result, nil
	case "show":
		var params protocol.IntegrationShowParams
		if err := readIntegrationParams(paramsFile, in, &params); err != nil {
			return nil, err
		}
		var result protocol.IntegrationShowResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodIntegrationShow, params, &result); err != nil {
			return result, err
		}
		return result, nil
	case "verify":
		var params protocol.IntegrationVerifyParams
		if err := readIntegrationParams(paramsFile, in, &params); err != nil {
			return nil, err
		}
		var result protocol.IntegrationVerifyResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodIntegrationVerify, params, &result); err != nil {
			return result, err
		}
		return result, nil
	case "request-delivery":
		var params protocol.IntegrationRequestDeliveryParams
		if err := readIntegrationParams(paramsFile, in, &params); err != nil {
			return nil, err
		}
		var result protocol.IntegrationRequestDeliveryResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodIntegrationRequestDelivery, params, &result); err != nil {
			return result, err
		}
		return result, nil
	case "deliver":
		var params protocol.IntegrationDeliverParams
		if err := readIntegrationParams(paramsFile, in, &params); err != nil {
			return nil, err
		}
		var result protocol.IntegrationDeliverResult
		if err := coordtransport.Call(ctx, socket, protocol.MethodIntegrationDeliver, params, &result); err != nil {
			return result, err
		}
		return result, nil
	default:
		// The closed name check above makes this unreachable, but keeping the
		// default ensures a future edit cannot accidentally open generic RPC.
		return nil, usageError("unknown integration command: " + name)
	}
}

func parseIntegrationFlags(name string, args []string) (string, error) {
	fs := newFlags("integration " + name)
	paramsFile := fs.String("params-file", "", "read bounded JSON parameters from a file, or - for stdin")
	_ = fs.Bool("json", false, "machine-readable JSON envelope")
	if err := parseFlags(fs, args); err != nil {
		return "", err
	}
	if *paramsFile == "" {
		return "", usageError("integration " + name + " requires --params-file FILE or --params-file -")
	}
	if fs.NArg() != 0 {
		return "", usageError("integration " + name + " takes only --params-file and --json")
	}
	return *paramsFile, nil
}

func readIntegrationParams(file string, in io.Reader, out any) error {
	var (
		data []byte
		err  error
	)
	if file == "-" {
		data, err = io.ReadAll(io.LimitReader(in, maxIntegrationParamsBytes+1))
		if err != nil {
			return fmt.Errorf("read integration params: %w", err)
		}
	} else {
		f, openErr := os.Open(file)
		if openErr != nil {
			return fmt.Errorf("read integration params file: %w", openErr)
		}
		data, err = io.ReadAll(io.LimitReader(f, maxIntegrationParamsBytes+1))
		closeErr := f.Close()
		if err != nil {
			return fmt.Errorf("read integration params file: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("close integration params file: %w", closeErr)
		}
	}
	if len(data) > maxIntegrationParamsBytes {
		return usageError(fmt.Sprintf("integration params exceed %d bytes", maxIntegrationParamsBytes))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return usageError("invalid integration params JSON: " + err.Error())
	}
	return nil
}
