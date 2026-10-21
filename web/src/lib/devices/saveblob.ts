// Hands bytes to the browser as a file (plan m2x step 8): an anchor with `download`, clicked once.
// The one URL this UI sets from script is the blob URL made here from bytes the page already holds;
// no daemon or device value ever becomes a URL (http-websocket.md §Security baseline rule 8).
// `web/sinks_test.go` allows exactly this line (`blobSave`).
export function saveBlob(blob: Blob, name: string): void {
	const url = URL.createObjectURL(blob);
	const a = document.createElement('a');
	a.href = url;
	a.download = name;
	a.click();
	setTimeout(() => URL.revokeObjectURL(url), 0);
}
