package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// changeReport is what a mutating command emits under --json: one record per
// thing it changed, so a script can confirm what was applied without parsing
// the prose written for a terminal.
type changeReport struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	ID     int64  `json:"id,omitempty"`
}

// ChangeError reports the reference an operation failed on. A command applies a
// list of references in order, so naming the one that failed is what tells the
// operator how far it got.
type ChangeError struct {
	Ref string
	Err error
}

func (e *ChangeError) Error() string { return e.Ref + ": " + e.Err.Error() }

func (e *ChangeError) Unwrap() error { return e.Err }

// resolver turns a user-supplied reference into an ID.
type resolver func(context.Context, *client.Client, string) (int64, error)

// resolveDeviceRef adapts Client.ResolveDevice to the resolver signature the
// shared command helpers take.
func resolveDeviceRef(ctx context.Context, api *client.Client, ref string) (int64, error) {
	return api.ResolveDevice(ctx, ref)
}

// resolveProfileRef adapts Client.ResolveProfile.
func resolveProfileRef(ctx context.Context, api *client.Client, ref string) (int64, error) {
	return api.ResolveProfile(ctx, ref)
}

// resolveMIBRef adapts Client.ResolveMIB.
func resolveMIBRef(ctx context.Context, api *client.Client, ref string) (int64, error) {
	return api.ResolveMIB(ctx, ref)
}

// bulkOp describes a command that applies one operation to many references,
// such as "netmon device mib add <device> <mib>...". Attaching MIBs to a device,
// MIBs to a profile and profiles to a device differ only in these fields.
type bulkOp struct {
	// kind names what the references are, for --json output.
	kind string

	// verb is the past-tense word the terminal output reports, "Assigned" or
	// "Removed"; its lower-case form is the action in --json.
	verb string

	resolveOwner  resolver
	resolveTarget resolver
	apply         func(context.Context, *client.Client, int64, int64) error
}

// runBulk resolves an owner and a list of references and applies the operation
// to each pair, so a whole MIB or profile set can be attached in one command.
//
// Progress is printed as it goes, because a long list against a slow server
// should show what it has done rather than fall silent and report in one burst.
// Under --json the records are emitted together at the end instead, since a
// half-written document is not a document.
func (e *Env) runBulk(op bulkOp, ownerRef string, refs []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), e.Opts.Timeout)
	defer cancel()

	api, err := e.Client()
	if err != nil {
		return err
	}
	ownerID, err := op.resolveOwner(ctx, api, ownerRef)
	if err != nil {
		return err
	}

	changes := make([]changeReport, 0, len(refs))
	for _, ref := range refs {
		id, resolveErr := op.resolveTarget(ctx, api, ref)
		if resolveErr != nil {
			return resolveErr
		}
		if applyErr := op.apply(ctx, api, ownerID, id); applyErr != nil {
			return &ChangeError{Ref: ref, Err: applyErr}
		}

		changes = append(changes, changeReport{
			Action: strings.ToLower(op.verb), Kind: op.kind, Ref: ref, ID: id,
		})
		if !e.Opts.JSON {
			fmt.Fprintf(e.Out, "%s %s\n", op.verb, ref)
		}
	}

	if e.Opts.JSON {
		return writeJSON(e.Out, changes)
	}
	return nil
}
