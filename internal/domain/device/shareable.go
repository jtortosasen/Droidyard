package device

import "context"

// ShareableDevice is one device of `adb devices -l` as `droidyard share` sees it (ADR-0108
// §1): whatever the physical-device policy says about it, since sharing is what admits it.
type ShareableDevice struct {
	Device string
	State  string
	Kind   string
	Model  string
	// Duplicate is whether another entry of the same listing reports the same serial
	// (ADR-0103 §1): the allowlist names serials, so such a device is not shared.
	Duplicate bool
}

// Shareable lists every device adb reports, physical or not, admitted or not, with what
// the share path needs to judge it. It is read by hosting.share only; every other
// listing hides what the policy does not admit. Its errors are the contract's
// DEVICE_ADB_NOT_FOUND and DEVICE_ADB_FAILED, or ctx's own.
func (a *ADB) Shareable(ctx context.Context) ([]ShareableDevice, error) {
	res := a.Resolve()
	if !res.Found {
		return nil, adbNotFound(res, "hosting.share")
	}
	listed, err := a.listAll(ctx, res.Path)
	if err != nil {
		return nil, err
	}
	out := make([]ShareableDevice, 0, len(listed))
	for _, d := range listed {
		out = append(out, ShareableDevice{Device: d.Device, State: d.State, Kind: d.Kind, Model: d.Model, Duplicate: serialCount(listed, d.Device) > 1})
	}
	return out, nil
}
