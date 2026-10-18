<script lang="ts">
	import { untrack } from 'svelte';
	import type { ConnectionState } from '../api/client';
	import { browserCodec, VideoPacketDecoder, type DecodedFrame } from './decoder';
	import type { LiveViewModel, LiveViewView, StopReason } from './liveview';
	import { ScreenSocket } from './stream';
	import { GestureRecorder, InputSender, NAV_KEYS, toFrame, type InputClient, type NavKey } from './input';

	// The device's screen (plan m2p step 5): the model decides what is watched; this draws.
	// A decoded frame replaces the one waiting to be drawn, and the canvas shows the newest
	// one on each animation frame. `data-packets` counts the messages received and
	// `data-frames` the frames decoded, for the level-5 check.
	interface Props {
		model: LiveViewModel;
		token: string;
		connection: ConnectionState;
		/** The panel's region is open. */
		visible: boolean;
		/** Where the input goes (plan m2p step 7). */
		input: InputClient;
	}
	let { model, token, connection, visible, input }: Props = $props();

	let view = $state.raw<LiveViewView>(untrack(() => model.view));
	let pageHidden = $state(document.hidden);
	let root: HTMLElement | undefined;
	let canvas: HTMLCanvasElement | undefined;
	let frames = 0;
	let packets = 0;

	$effect(() => {
		const m = model;
		const read = () => {
			view = m.view;
		};
		read();
		return m.subscribe(read);
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
		const socket = new ScreenSocket(
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
		return () => {
			socket.close();
			decoder.close();
			if (raf !== 0) cancelAnimationFrame(raf);
			latest?.close();
		};
	});

	// Input (plan m2p step 7): pointer events with capture, so a mouse, a pen and a finger
	// work alike. Coordinates are the frame's, never the letterbox bars'.
	const sender = $derived(new InputSender(input, () => view.device));
	let problem = $state('');
	let text = $state('');
	let overFrame = $state(false);
	let recorder: GestureRecorder | null = null;

	const frameSize = () => ({ width: canvas?.width ?? 0, height: canvas?.height ?? 0 });
	function report(p: Promise<string | null>): Promise<boolean> {
		return p.then((e) => {
			problem = e ?? '';
			return e === null;
		});
	}
	function down(e: PointerEvent) {
		if (!canvas || e.button !== 0) return;
		recorder = GestureRecorder.start(e.clientX, e.clientY, e.timeStamp, canvas.getBoundingClientRect(), frameSize());
		if (recorder) canvas.setPointerCapture(e.pointerId);
	}
	function move(e: PointerEvent) {
		if (!canvas) return;
		if (recorder) recorder.move(e.clientX, e.clientY, e.timeStamp);
		else overFrame = toFrame(e.clientX, e.clientY, canvas.getBoundingClientRect(), frameSize()) !== null;
	}
	function up(e: PointerEvent) {
		const r = recorder;
		recorder = null;
		if (r) void report(sender.gesture(r.end(e.clientX, e.clientY, e.timeStamp)));
	}
	function press(key: NavKey) {
		void report(sender.key(key));
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
	};
</script>

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
	<canvas
		bind:this={canvas}
		hidden={view.state !== 'live'}
		aria-label="Device screen"
		class:acts={overFrame}
		onpointerdown={down}
		onpointermove={move}
		onpointerup={up}
		onpointercancel={() => (recorder = null)}
		onpointerleave={() => (overFrame = false)}
	></canvas>
	{#if view.state === 'live'}
		<div class="keys">
			{#each NAV_KEYS as k (k.key)}
				<button type="button" onclick={() => press(k.key)}>{k.label}</button>
			{/each}
		</div>
		<form onsubmit={send}>
			<input type="text" bind:value={text} maxlength="500" aria-label="Text to type into the focused field" />
			<button type="submit" disabled={text === ''}>Send</button>
		</form>
		{#if problem !== ''}
			<p role="alert">{problem}</p>
		{/if}
	{/if}
</section>

<style>
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
	canvas[hidden] {
		display: none;
	}
</style>
