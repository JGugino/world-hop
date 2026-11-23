<script lang="ts">
	import { onMount } from 'svelte';
	import { getSources, addSource, removeSource, pickFolder, type Source, type SourceKind } from '$lib/api';

	let sources = $state<Source[] | null>(null);
	let error = $state('');
	let kind = $state<SourceKind>('saves');
	let path = $state('');
	let adding = $state(false);
	let picking = $state(false);
	let pickerMissing = $state(false);
	let confirmRemove = $state<string | null>(null);

	const key = (s: Source) => `${s.kind}:${s.path}`;

	async function load() {
		try {
			sources = await getSources();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function add(e: SubmitEvent) {
		e.preventDefault();
		adding = true;
		error = '';
		try {
			sources = await addSource(kind, path);
			path = '';
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		}
		adding = false;
	}

	async function browse() {
		picking = true;
		error = '';
		try {
			const r = await pickFolder();
			if (r.path) path = r.path;
		} catch {
			// No native dialog on this system: the text box still works.
			pickerMissing = true;
		}
		picking = false;
	}

	async function remove(s: Source) {
		error = '';
		try {
			sources = await removeSource(s.kind, s.path);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		}
		confirmRemove = null;
	}

	onMount(load);
</script>

<section class="card">
	<h2>Add a saves location</h2>
	<p class="muted small intro">
		Add as many as you like. A <b>saves folder</b> syncs every world inside it, including worlds you
		create later. A <b>single world</b> syncs just that one folder.
	</p>
	<form onsubmit={add}>
		<div class="kinds" role="radiogroup" aria-label="Save type">
			<label class="kind" class:on={kind === 'saves'}>
				<input type="radio" bind:group={kind} value="saves" />
				<span class="kind-text"><b>Saves folder</b><small>Every world inside</small></span>
			</label>
			<label class="kind" class:on={kind === 'world'}>
				<input type="radio" bind:group={kind} value="world" />
				<span class="kind-text"><b>Single world</b><small>Just that one folder</small></span>
			</label>
		</div>
		<div class="row">
			<input
				type="text"
				bind:value={path}
				aria-label="Folder path"
				placeholder={kind === 'saves' ? '/home/you/.minecraft/saves' : '/home/you/.minecraft/saves/My World'}
				required />
			{#if !pickerMissing}
				<button type="button" class="secondary" onclick={browse} disabled={picking}>{picking ? 'Choosing…' : 'Browse…'}</button>
			{/if}
			<button disabled={adding || !path.trim()}>{adding ? 'Adding…' : 'Add'}</button>
		</div>
		{#if pickerMissing}
			<p class="muted small hint">A folder picker isn't available on this system. Paste the folder path instead.</p>
		{:else}
			<p class="muted small hint">Click Browse to pick the folder, then Add.</p>
		{/if}
	</form>
	{#if error}<p class="bad">{error}</p>{/if}
</section>

<section class="card">
	<h2>Synced saves {#if sources && sources.length}<span class="count">{sources.length}</span>{/if}</h2>
	{#if sources === null}
		<p class="muted">Loading…</p>
	{:else if sources.length === 0}
		<div class="empty">
			<div class="empty-icon" aria-hidden="true"></div>
			<p class="empty-title">Nothing is being synced yet</p>
			<p class="muted small">Add a saves folder or a single world above and WorldHop will start syncing it.</p>
		</div>
	{:else}
		<ul>
			{#each sources as s (key(s))}
				<li class:broken={!!s.error}>
					<div class="info">
						<div class="meta">
							<span class="tag {s.kind}">{s.kind === 'saves' ? 'Saves folder' : 'Single world'}</span>
							{#if s.launcher}<span class="launcher">{s.launcher}</span>{/if}
						</div>
						<code class="path" title={s.path}>{s.path}</code>
						{#if s.error}
							<div class="bad small">{s.error}</div>
						{:else}
							<div class="count-line muted small">{s.worlds} world{s.worlds === 1 ? '' : 's'}</div>
						{/if}
					</div>
					<div class="actions">
						{#if confirmRemove === key(s)}
							<button class="danger" onclick={() => remove(s)}>Stop syncing</button>
							<button class="secondary" onclick={() => (confirmRemove = null)}>Cancel</button>
						{:else}
							<button class="secondary" onclick={() => (confirmRemove = key(s))}>Remove</button>
						{/if}
					</div>
				</li>
			{/each}
		</ul>
		<p class="muted small foot">Removing a save stops syncing it. Your files and cloud copies are not deleted.</p>
	{/if}
</section>

<style>
	.intro { margin-bottom: 14px; }
	.kinds { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-bottom: 12px; }
	.kind { display: flex; gap: 10px; align-items: center; padding: 10px 12px; background: #1f2327; border: 2px solid #000; cursor: pointer; box-shadow: inset 2px 2px 0 #14171a, inset -2px -2px 0 #333a41; }
	.kind:hover { background: #262b30; }
	.kind.on { background: #22301e; box-shadow: inset 2px 2px 0 #14200f, inset -2px -2px 0 #3d9a2b; }
	.kind:has(input:focus-visible) { outline: 3px solid var(--warn); outline-offset: 2px; }
	.kind-text { display: grid; gap: 2px; }
	.kind-text small { color: var(--muted); font-size: 0.8rem; }
	.row { display: flex; gap: 8px; align-items: center; }
	form .row input { flex: 1; min-width: 0; padding: 9px 12px; }
	.count { font-size: 0.7rem; background: #111315; border: 2px solid #000; padding: 3px 8px; margin-left: 6px; color: var(--muted); vertical-align: middle; }
	ul { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; }
	li { display: flex; justify-content: space-between; gap: 16px; align-items: center; padding: 12px 14px; background: #1f2327; border: 2px solid #000; box-shadow: inset 2px 2px 0 #14171a, inset -2px -2px 0 #333a41; transition: background 0.1s; }
	li:hover, li:focus-within { background: #262b30; }
	li.broken { box-shadow: inset 4px 0 0 var(--bad), inset -2px -2px 0 #333a41; }
	.info { min-width: 0; display: grid; gap: 6px; }
	.meta { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
	.tag { font-size: 0.8rem; font-weight: 600; padding: 2px 9px; border: 2px solid #000; background: #2d3a28; color: var(--ok); }
	.tag.world { background: #26323d; color: var(--info); }
	.launcher { color: var(--muted); font-size: 0.85rem; }
	.path { display: block; padding: 5px 9px; background: #111315; border: 2px solid #000; color: #dfe3e6; overflow-wrap: anywhere; }
	.actions { display: flex; gap: 8px; flex-shrink: 0; }
	.foot { margin-top: 14px; }
	.empty { text-align: center; padding: 22px 12px; border: 2px dashed var(--line); }
	.empty-icon { width: 32px; height: 32px; margin: 0 auto 12px; background: var(--dirt); background-size: 64px; border: 2px solid #000; box-shadow: inset 0 8px 0 #4a9a35; image-rendering: pixelated; }
	.empty-title { margin: 0 0 4px; font-weight: 600; }
	.bad { color: var(--bad); }
	.hint { margin-top: 8px; }
	@media (max-width: 560px) {
		.kinds { grid-template-columns: 1fr; }
		li { flex-direction: column; align-items: stretch; }
		.actions { justify-content: flex-end; }
		.row { flex-wrap: wrap; }
	}
</style>
