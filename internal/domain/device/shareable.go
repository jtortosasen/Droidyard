package device

import (
	"context"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// ShareableDevice is one device of `adb devices -l` as `droidyard share` sees it (ADR-0108
// §1): whatever the physical-device policy says about it, since sharing is what admits it.
type ShareableDevice struct {
	Device string
	State  string
	Kind   string
	Model  string
	// USB is whether adb reported the device on a usb: transport; false is a device
	// reached over the network.
	USB bool
	// Manufacturer is ro.product.manufacturer, read for an online physical device listed
	// once and only within detailsTimeout; empty when it did not answer. It is the device's
	// own text, cleaned of control characters and bounded, as Model is.
	Manufacturer string
	// Duplicate is whether another entry of the same listing reports the same serial
	// (ADR-0103 §1): the allowlist names serials, so such a device is not shared.
	Duplicate bool
}

// manufacturerOutputCap bounds `getprop ro.product.manufacturer`'s output.
const manufacturerOutputCap = 1 << 10

// Shareable lists every device adb reports, physical or not, admitted or not, with what
// the share path needs to judge it. It is read by hosting.share only; every other
// listing hides what the policy does not admit. The manufacturer of each online physical
// device that is listed once is read in parallel, under detailsTimeout together, when
// manufacturers is true (the listing of the candidates; a share of a named serial has no use
// for it); a serial that several devices report is never addressed, since adb cannot tell them
// apart. Its errors are the contract's DEVICE_ADB_NOT_FOUND and DEVICE_ADB_FAILED, or ctx's own.
func (a *ADB) Shareable(ctx context.Context, manufacturers bool) ([]ShareableDevice, error) {
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
		out = append(out, ShareableDevice{Device: d.Device, State: d.State, Kind: d.Kind, Model: cleanText(d.Model, 48), USB: d.usb, Duplicate: serialCount(listed, d.Device) > 1})
	}
	dctx, cancel := context.WithTimeout(ctx, detailsTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i := range out {
		d := &out[i]
		if !manufacturers || d.Kind != KindPhysical || d.State != StateDevice || d.Duplicate {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := a.Run(dctx, res.Path, detailsTimeout, manufacturerOutputCap, "-s", d.Device, "shell", "getprop", "ro.product.manufacturer")
			if err == nil && o.ExitCode == 0 {
				d.Manufacturer = cleanText(string(o.Stdout), 32)
			}
		}()
	}
	wg.Wait()
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	return out, nil
}

// cleanText is s as a single line of printable text of at most n runes: what a device says
// about itself is untrusted and ends up on a terminal.
func cleanText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return strings.TrimSpace(s)
}
