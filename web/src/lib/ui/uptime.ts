// The status bar's uptime ticks (m2e step 7): `system.health` answers once, so the
// shown value is the answered uptime plus the local time elapsed since it arrived.

/** The uptime to show: the answered seconds plus whole seconds elapsed locally; never backwards. */
export function liveUptime(answered: number, receivedAt: number, now: number): number {
	return answered + Math.max(0, Math.floor((now - receivedAt) / 1000));
}

export function formatUptime(seconds: number): string {
	const h = Math.floor(seconds / 3600);
	const m = Math.floor((seconds % 3600) / 60);
	return h > 0 ? `${h} h ${m} min` : m > 0 ? `${m} min ${seconds % 60} s` : `${seconds} s`;
}
