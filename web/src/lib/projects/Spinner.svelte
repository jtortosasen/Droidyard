<script lang="ts">
	// A small spinner for "an agent is working". Decoration: the label beside it says so.
	interface Props {
		size?: number;
	}
	let { size = 12 }: Props = $props();
</script>

<span class="spinner" style:--size="{size}px" aria-hidden="true"></span>

<style>
	.spinner {
		display: inline-block;
		flex: none;
		width: var(--size);
		height: var(--size);
		border: 2px solid var(--border);
		border-top-color: var(--accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.spinner {
			animation-duration: 3s;
		}
	}
</style>
