<script lang="ts">
	import { activate, BRANCH_MENU, type MenuId } from './controls';

	// The entries of a branch's menu, a local or a remote-tracking one, IntelliJ's eight (plan m2n
	// step 6; the owner's items 7 and 8). Each is shown disabled until the part that enables it
	// (controls.ts), with a title that says which; a click goes through `activate`, and an enabled
	// entry reports its id (branchops.ts says what it calls). The branch name is git's text.
	interface Props {
		/** The branch the menu is of, as the list shows it. */
		name: string;
		/** An enabled entry was chosen. */
		onchoose: (id: MenuId) => void;
	}
	let { name, onchoose }: Props = $props();
</script>

<p class="menu-title" data-menu-branch>{name}</p>
{#each BRANCH_MENU as c (c.id)}
	<button type="button" class="item" role="menuitem" data-menu-item={c.id} disabled={!c.enabled} title={c.title} onclick={() => activate(c, () => onchoose(c.id))}>
		{c.label}
	</button>
{/each}

<style>
	.menu-title {
		unicode-bidi: isolate;
		margin: var(--sp-2) var(--sp-3);
		max-width: 28ch;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: var(--fs-small);
		font-weight: 600;
		color: var(--text-2);
	}
	.item {
		justify-content: flex-start;
		text-align: left;
		border-color: transparent;
		background: transparent;
	}
</style>
