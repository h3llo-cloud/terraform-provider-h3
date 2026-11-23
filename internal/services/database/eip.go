package database

import (
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// validateEIP: eip_id selects an existing project EIP as the public address,
// so it needs white_ip = true (white_ip defaults to false when omitted).
func validateEIP(eipID types.String, whiteIP types.Bool) error {
	if eipID.IsNull() || eipID.IsUnknown() || whiteIP.IsUnknown() {
		return nil
	}
	if whiteIP.IsNull() || !whiteIP.ValueBool() {
		return errors.New("eip_id selects an existing Elastic IP as the database public IP; set white_ip = true or remove eip_id")
	}
	return nil
}

// planEIPID returns the planned eip_id when it is not configured: null without
// a public IP, the current EIP while the public IP stays on, unknown when a
// new EIP is going to be leased. A configured value is planned as is.
func planEIPID(cfg types.String, planWhiteIP types.Bool, state *DatabaseResourceModel) types.String {
	if !cfg.IsNull() {
		return cfg
	}
	if planWhiteIP.IsUnknown() {
		return types.StringUnknown()
	}
	if !planWhiteIP.ValueBool() {
		return types.StringNull()
	}
	if state != nil && state.WhiteIP.ValueBool() && !state.EIPID.IsUnknown() {
		return state.EIPID
	}
	return types.StringUnknown()
}

type whiteIPStep struct {
	Action string // "attach" or "detach"
	EIPID  string // body of attach; "" leases a new EIP
}

// whiteIPSteps returns the h3db calls that move the public address from
// (oldWhite, oldEIP) to (newWhite, newEIP). newEIP is "" when not chosen.
func whiteIPSteps(oldWhite, newWhite bool, oldEIP, newEIP string) []whiteIPStep {
	switch {
	case oldWhite && !newWhite:
		return []whiteIPStep{{Action: "detach"}}
	case !oldWhite && newWhite:
		return []whiteIPStep{{Action: "attach", EIPID: newEIP}}
	case oldWhite && newWhite && newEIP != "" && newEIP != oldEIP:
		return []whiteIPStep{{Action: "detach"}, {Action: "attach", EIPID: newEIP}}
	}
	return nil
}

// eipFromAPI maps netConfig.eipId: null without a public IP; the reported ID
// when present; otherwise (older h3db) the prior value is kept.
func eipFromAPI(db *Database, prior types.String) types.String {
	if !db.WhiteIP {
		return types.StringNull()
	}
	if db.NetConfig.EIPID != nil && *db.NetConfig.EIPID != "" {
		return types.StringValue(*db.NetConfig.EIPID)
	}
	if prior.IsUnknown() {
		return types.StringNull()
	}
	return prior
}

// deleteQuery: h3db keeps a pool EIP on delete unless releaseEip=true.
func deleteQuery(release bool) map[string]string {
	if release {
		return map[string]string{"releaseEip": "true"}
	}
	return nil
}
