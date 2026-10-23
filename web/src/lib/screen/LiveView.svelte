<script lang="ts">
	import { untrack } from 'svelte';
	import type { ConnectionState } from '../api/client';
	import { browserCodec, VideoPacketDecoder, type DecodedFrame } from './decoder';
	import { failureText } from '../build/failure';
	import type { LiveViewModel, LiveViewView, StopReason } from './liveview';
	import type { QualityView, RemoteQualityModel } from './quality';
	import { ScreenSocket } from './stream';
	import { encodeKey } from './control';
	import { InputSender, NAV_KEYS, gated, PointerInput, WheelAccumulator, toFrame, type InputClient, type NavKey } from './input';

	// The device's screen (plan m2p step 5): the model decides what is watched; this draws.
	// The input (ADR-0117) travels this stream's own socket as binary frames when the watch's
	// result said `control`. A decoded frame replaces the one waiting to be drawn, and the canvas shows the newest
	// one on each animation frame. `data-packets` counts the messages received and
	// `data-frames` the frames decoded, for the level-5 check.
	interface Props {
		model: LiveViewModel;
		/** The remote's quality for a remote device's view: the Fast toggle. */
		quality: RemoteQualityModel;
		token: string;
		connection: ConnectionState;
		/** The panel's region is open. */
		visible: boolean;
		/** Where the text goes: the one input that is still a call. */
		input: InputClient;
	}
	let { model, quality, token, connection, visible, input }: Props = $props();

	let view = $state.raw<LiveViewView>(untrack(() => model.view));
	let q = $state.raw<QualityView>(untrack(() => quality.view));
	let pageHidden = $state(document.hidden);
	let root: HTMLElement | undefined;
	let canvas: HTMLCanvasElement | undefined;
	let frames = 0;
	let packets = 0;
	let socket: ScreenSocket | null = null;
	const emit = gated(
		() => view.control,
		(bytes) => socket?.send(bytes),
	);
	const pointer = new PointerInput(emit);
	const wheel = new WheelAccumulator();
	let wheelRaf = 0;

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
	});

	$effect(() => {
		const m = quality;
		const read = () => {
			q = m.view;
		};
		read();
		return m.subscribe(read);
	});

	// The remote's mark is read at each answered watch of a remote device (a local one reads nothing).
	const generation = $derived(view.generation);
	$effect(() => {
		if (generation > 0) untrack(() => void quality.read());
	});

	$effect(() => model.setActive(visible && !pageHidden));
	$effect(() => model.onConnection(connection));

	// One socket and one decoder per answered watch: the key changes with each.
	const streamKey = $derived(view.streamId === '' ? '' : `${view.generation}:${view.streamId}`);

	$effect(() => {
		if (streamKey === '') return;
		const m = untrack(() => model);
		const { streamId, generation } = untrack(() => view);
		const codec = browserCodec();
		if (codec === null) return;
		let latest: DecodedFrame | null = null;
		let raf = 0;

		function draw() {
			raf = 0;
			const f = latest;
			latest = null;
			if (!f || !canvas) return;
			if (canvas.width !== f.displayWidth || canvas.height !== f.displayHeight) {
				canvas.width = f.displayWidth;
				canvas.height = f.displayHeight;
			}
			canvas.getContext('2d')?.drawImage(f as unknown as CanvasImageSource, 0, 0);
			f.close();
		}
		const decoder = new VideoPacketDecoder(codec, {
			frame: (f) => {
				frames++;
				if (root) root.dataset.frames = String(frames);
				latest?.close();
				latest = f;
				if (raf === 0) raf = requestAnimationFrame(draw);
				m.frameDecoded();
			},
			error: (message) => m.decoderError(message),
		});
		const s = new ScreenSocket(
			{ url: `ws://${window.location.host}/v1/screen`, token, streamId, createSocket: (u) => new WebSocket(u) },
			{
				packet: (p) => {
					packets++;
					if (root) root.dataset.packets = String(packets);
					decoder.push(p);
				},
				closed: (code) => m.socketClosed(code, generation),
			},
		);
		socket = s;
		return () => {
			socket = null;
			pointer.reset();
			wheel.reset();
			if (wheelRaf !== 0) cancelAnimationFrame(wheelRaf);
			wheelRaf = 0;
			s.close();
			decoder.close();
			if (raf !== 0) cancelAnimationFrame(raf);
			latest?.close();
		};
	});

	// Input: pointer events with capture, so a mouse, a pen and a finger work alike. Each
	// event is a frame of the wire at once; coordinates are the frame's, never the bars'.
	const sender = $derived(new InputSender(input, () => view.device));
	let problem = $state('');
	let text = $state('');
	let overFrame = $state(false);

	const frameSize = () => ({ width: canvas?.width ?? 0, height: canvas?.height ?? 0 });
	function report(p: Promise<string | null>): Promise<boolean> {
		return p.then((e) => {
			problem = e ?? '';
			return e === null;
		});
	}
	function down(e: PointerEvent) {
		if (!canvas || !view.control || e.button !== 0) return;
		if (pointer.down(e.pointerId, e.clientX, e.clientY, canvas.getBoundingClientRect(), frameSize())) {
			canvas.setPointerCapture(e.pointerId);
		}
	}
	function move(e: PointerEvent) {
		if (!canvas || !view.control) return;
		if (pointer.open) pointer.move(e.pointerId, e.clientX, e.clientY, canvas.getBoundingClientRect(), frameSize());
		else overFrame = toFrame(e.clientX, e.clientY, canvas.getBoundingClientRect(), frameSize()) !== null;
	}
	function up(e: PointerEvent) {
		if (canvas) pointer.up(e.pointerId, e.clientX, e.clientY, canvas.getBoundingClientRect(), frameSize());
	}
	function flushWheel() {
		wheelRaf = 0;
		const frame = wheel.take();
		if (frame !== null) emit(frame);
	}
	// A non-passive listener, so the page does not scroll under the wheel.
	function wheelInput(node: HTMLCanvasElement) {
		const onwheel = (e: WheelEvent) => {
			if (!view.control) return;
			const at = toFrame(e.clientX, e.clientY, node.getBoundingClientRect(), frameSize());
			if (at === null) return;
			e.preventDefault();
			wheel.add(e.deltaX, e.deltaY, e.deltaMode, at);
			if (wheelRaf === 0) wheelRaf = requestAnimationFrame(flushWheel);
		};
		node.addEventListener('wheel', onwheel, { passive: false });
		return () => node.removeEventListener('wheel', onwheel);
	}
	function press(key: NavKey) {
		emit(encodeKey(key));
	}
	async function send(e: SubmitEvent) {
		e.preventDefault();
		if (text === '') return;
		if (await report(sender.text(text))) text = '';
	}

	const WHY: Record<StopReason, string> = {
		deviceGone: 'The device went away.',
		exited: 'The screen server on the device ended.',
		failed: 'The video stream failed.',
		detached: 'The stream connection was lost.',
		renewed: 'The view restarted for its new quality.',
	};

	// What the alert line says: a failed input, else a failed write of the remote's quality.
	const alertText = $derived(problem !== '' && view.state === 'live' ? problem : q.error ? failureText(q.error) : '');
</script>

{#snippet fast()}
	{#if q.shown}
		<button
			type="button"
			class="fast"
			aria-pressed={q.fast}
			disabled={q.busy}
			title="A lighter stream for a slow link; the view restarts"
			onclick={() => void quality.setFast(!q.fast)}>Fast</button
		>
	{/if}
{/snippet}

<svelte:document onvisibilitychange={() => (pageHidden = document.hidden)} />

<section bind:this={root} data-frames="0" data-packets="0">
	<h2>Live view</h2>
	{#if view.state === 'unsupported'}
		<p>This browser cannot decode video (WebCodecs), so the live view is not available.</p>
	{:else if view.state === 'stopped'}
		<p>
			{view.reason ? WHY[view.reason] : ''}
			{view.cause}
			<button type="button" onclick={() => model.restart()}>Restart</button>
		</p>
	{:else if view.state === 'idle'}
		<p>Select a device first.</p>
	{:else if view.state === 'starting'}
		<p>Starting...</p>
	{/if}
	<div class="frame">
		<canvas
			bind:this={canvas}
			hidden={view.state !== 'live'}
			aria-label="Device screen"
			class:acts={overFrame && view.control}
			{@attach wheelInput}
			onpointerdown={down}
			onpointermove={move}
			onpointerup={up}
			onpointercancel={(e) => pointer.cancel(e.pointerId)}
			onlostpointercapture={(e) => pointer.cancel(e.pointerId)}
			onpointerleave={() => (overFrame = false)}
		></canvas>
		{#if view.state === 'live' && view.secure}
			<p class="secure" role="status">
				The focused window is secure (FLAG_SECURE): Android shows it black.
			</p>
		{/if}
	</div>
	{#if view.state === 'live'}
		{#if !view.control}
			<p role="status">This view is watch-only: the host does not take input on it.</p>
		{/if}
		<div class="keys">
			{#each NAV_KEYS as k (k.key)}
				<button type="button" disabled={!view.control} onclick={() => press(k.key)}>{k.label}</button>
			{/each}
			{@render fast()}
		</div>
		<form onsubmit={send}>
			<input type="text" bind:value={text} maxlength="500" aria-label="Text to type into the focused field" />
			<button type="submit" disabled={text === ''}>Send</button>
		</form>
	{:else if q.shown}
		<div class="keys">{@render fast()}</div>
	{/if}
	{#if alertText !== ''}
		<p role="alert">{alertText}</p>
	{/if}
</section>

<style>
	.frame {
		position: relative;
	}
	.secure {
		position: absolute;
		top: 0.5rem;
		left: 0.5rem;
		right: 0.5rem;
		margin: 0;
		padding: var(--sp-3) var(--sp-5);
		/* The tint over an opaque panel: the notice floats on the frame, which is black when it shows. */
		background: linear-gradient(var(--warning-soft), var(--warning-soft)), var(--bg-panel);
		color: var(--text);
		font-size: var(--fs-base);
		border: 0;
		border-left: 3px solid var(--warning);
		border-radius: 0;
		pointer-events: none;
	}
	canvas {
		display: block;
		width: 100%;
		max-height: 70vh;
		object-fit: contain;
		touch-action: none;
	}
	canvas.acts {
		cursor: crosshair;
	}
	.keys,
	form {
		display: flex;
		gap: 0.5rem;
		margin-top: 0.5rem;
	}
	input {
		flex: 1;
		min-width: 0;
	}
	.fast {
		margin-left: auto;
		min-height: 24px;
		font-size: var(--fs-small);
		color: var(--text-2);
	}
	.fast[aria-pressed='true'] {
		color: var(--text);
		font-weight: 600;
	}
	canvas[hidden] {
		display: none;
	}
</style>
