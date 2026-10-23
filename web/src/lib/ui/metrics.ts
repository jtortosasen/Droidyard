// Pixel metrics that script and CSS must agree on (plan m3d step 4). A virtualised list does
// its window arithmetic in pixels, so its row height is a number here and reaches the CSS as
// `style:--log-row="{LOG_ROW_PX}px"`: one source, no 19 written in two places.

/** The height of a Logcat or Console row; scale.test.ts keeps it equal to `--lh-log` in tokens.css. */
export const LOG_ROW_PX = 19;
