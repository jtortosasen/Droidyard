package device

// Chrome builds the accessibility tree of a page only while an accessibility client is
// connected, within seconds of one connecting (plan m2x step 1, §When Chrome exposes a
// page's content): a dump taken before that has no page in it. The dump is right, the page
// is not there yet, and a parser that reads it as a finished page is wrong, so the dump says
// so. The sign differs by Android version, and it is a heuristic: a warning, not a fact.

const (
	// chromePackage and the classes whose shape is the sign.
	chromePackage    = "com.android.chrome"
	webViewClass     = "android.webkit.WebView"
	surfaceViewClass = "android.view.SurfaceView"
	// chromePageHiddenWarning is the fixed sentence uiDump and uiTree add to their warnings.
	chromePageHiddenWarning = "Chrome's page content may not be exposed yet; retry, or reload the page"
	// chromePageHiddenTag is the same fact as a recording's dump line carries it.
	chromePageHiddenTag = "chromePageHidden"
)

// chromePageHidden reports whether the parsed document looks like a Chrome window whose page
// is not exposed yet: Chrome's WebView with no children (API 31, observed), or no WebView of
// Chrome's at all while a SurfaceView of Chrome's is there (API 36, observed: the toolbar and
// the surface the page is drawn on). A WebView with children is a page; another app's
// childless one, or a Chrome window with neither, is not this (an app's own WebView and other
// browsers are not observed). It reads the raw parse, before the compact tree drops the nodes
// that carry nothing.
func chromePageHidden(roots []*rawNode) bool {
	var childless, webView, surface bool
	var walk func(nodes []*rawNode)
	walk = func(nodes []*rawNode) {
		for _, n := range nodes {
			if n.pkg == chromePackage {
				switch n.fullClass {
				case webViewClass:
					webView = true
					childless = childless || len(n.children) == 0
				case surfaceViewClass:
					surface = true
				}
			}
			walk(n.children)
		}
	}
	walk(roots)
	return childless || (!webView && surface)
}
