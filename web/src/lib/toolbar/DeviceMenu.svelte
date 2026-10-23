<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Smartphone from '@lucide/svelte/icons/smartphone';
	import type { DevicesModel, DevicesView } from '../devices/devices';
	import type { EmulatorsModel, EmulatorsView } from '../devices/emulators';
	import { chooseDeviceItem, deviceMenu, deviceTriggerText, type DeviceMenuItem } from './devicemenu';
	import { placeBelow } from './popover';

	// The toolbar's device menu (m2e D36) in a Popover API popover: the devices, then the
	// AVDs not running under "Start and run". The choice is DevicesModel's; an AVD is held
	// pending until ▶ starts it. Every field is a text node.
	interface Props {
		devices: DevicesModel;
		emulators: EmulatorsModel;
	}
	let { devices, emulators }: Props = $props();

	let dv = $state.raw<DevicesView | null>(null);
	let ev = $state.raw<EmulatorsView | null>(null);
	let pop = $state<HTMLDivElement>();
	let anchor = $state<HTMLButtonElement>();
	let top = $state(0);
	let left = $state(0);

	$effect(() => {
		const d = devices;
		const read = () => {
			dv = d.view;
		};
		read();
		return d.subscribe(read);
	});
	$effect(() => {
		const e = emulators;
		const read = () => {
			ev = e.view;
		};
		read();
		return e.subscribe(read);
	});

	const menu = $derived(dv ? deviceMenu(dv, ev?.avds ?? []) : null);

	function toggle() {
		if (!pop || !anchor) return;
		({ top, left } = placeBelow(anchor, 280));
		pop.togglePopover();
	}
	function choose(item: DeviceMenuItem) {
		chooseDeviceItem(devices, item);
		pop?.hidePopover();
	}
</script>

<div class="select">
	<button bind:this={anchor} type="button" class="trigger" title="Choose the device" aria-haspopup="menu" onclick={toggle}>
		<Smartphone size={14} />
		<span class="label">{dv ? deviceTriggerText(dv) : 'Devices'}</span>
		<ChevronDown size={12} />
	</button>
	<div bind:this={pop} popover="auto" class="pop" style:top="{top}px" style:left="{left}px" role="menu" aria-label="Device">
		{#if menu}
			{#each menu.devices as item (item.value)}
				{@render row(item)}
			{/each}
			{#if menu.devices.length === 0}<p class="note">No device is connected.</p>{/if}
			{#if menu.avds.length > 0}
				<h3>Start and run</h3>
				{#each menu.avds as item (item.value)}
					{@render row(item)}
				{/each}
			{/if}
		{/if}
	</div>
</div>

{#snippet row(item: DeviceMenuItem)}
	<button
		type="button"
		role="menuitemradio"
		aria-checked={item.current}
		class="item"
		class:current={item.current}
		disabled={item.disabled}
		title={item.reason ?? item.label}
		onclick={() => choose(item)}
	>
		{item.label}
	</button>
{/snippet}

<style>
	.select {
		display: inline-flex;
		min-width: 0;
	}
	.trigger {
		display: inline-flex;
		min-width: 0;
		overflow: hidden;
		font-family: var(--font-mono);
		font-size: var(--fs-small);
		align-items: center;
		gap: var(--sp-3);
		max-width: 200px;
	}
	.label {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.pop {
		position: fixed;
		inset: auto;
		margin: 0;
		width: 280px;
		max-height: 60vh;
		overflow: auto;
		padding: var(--sp-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-panel);
		background: var(--overlay-solid);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	/* The toolbar's `div` rule would show it closed: say it. */
	.pop:not(:popover-open) {
		display: none;
	}
	.pop:popover-open {
		display: flex;
		flex-direction: column;
		align-items: stretch;
		gap: var(--sp-1);
	}
	h3 {
		margin: var(--sp-3) var(--sp-3) 0;
		font-size: var(--fs-small);
		font-weight: 600;
		color: var(--text-2);
		text-transform: uppercase;
		letter-spacing: var(--tracking-label);
	}
	.note {
		margin: var(--sp-3);
		color: var(--text-3);
		font-size: var(--fs-small);
	}
	.item {
		justify-content: flex-start;
		text-align: left;
		border-color: transparent;
		background: transparent;
		overflow-wrap: anywhere;
	}
	.item.current {
		background: var(--bg-active);
	}
</style>
