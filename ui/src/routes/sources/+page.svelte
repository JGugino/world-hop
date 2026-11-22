<script lang="ts">
	import { onMount } from 'svelte';
	import { getSources, addSource, removeSource, type Source, type SourceKind } from '$lib/api';

	let sources = $state<Source[] | null>(null);
	let error = $state('');
	let kind = $state<SourceKind>('saves');
	let path = $state('');
	let adding = $state(false);
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
	<h2>Add a source</h2>
	<p class="muted small">
		Add as many as you like. A <b>saves folder</b> syncs every world inside it, including worlds you
		create later. A <b>single world</b> syncs just that one folder.
	</p>
	<form onsubmit={add}>
		<div class="kinds" role="radiogroup" aria-label="Source type">
			<label><input type="radio" bind:group={kind} value="saves" /> Saves folder</label>
			<label><input type="radio" bind:group={kind} value="world" /> Single world</label>
		</div>
		<div class="row">
			<input
				type="text"
				bind:value={path}
				aria-label="Folder path"
				placeholder={kind === 'saves' ? '/home/you/.minecraft/saves' : '/home/you/.minecraft/saves/My World'}
				required />
			<button disabled={adding || !path.trim()}>{adding ? 'Adding…' : 'Add'}</button>
		</div>
	</form>
	{#if error}<p class="bad">{error}</p>{/if}
</section>

<section class="card">
	<h2>Synced sources</h2>
	{#if sources === null}
		<p class="muted">Loading…</p>
	{:else if sources.length === 0}
		<p class="muted">Nothing is being synced. Add a saves folder or a world above.</p>
	{:else}
		<ul>
			{#each sources as s (key(s))}
				<li>
					<div class="info">
						<span class="tag">{s.kind === 'saves' ? 'Saves folder' : 'Single world'}</span>
						{#if s.launcher}<span class="muted small"> · {s.launcher}</span>{/if}
						<div><code>{s.path}</code></div>
						{#if s.error}
							<div class="bad small">{s.error}</div>
						{:else}
							<div class="muted small">{s.worlds} world{s.worlds === 1 ? '' : 's'}</div>
						{/if}
					</div>
					{#if confirmRemove === key(s)}
						<span class="row">
							<button class="danger" onclick={() => remove(s)}>Stop syncing</button>
							<button class="secondary" onclick={() => (confirmRemove = null)}>Cancel</button>
						</span>
					{:else}
						<button class="secondary" onclick={() => (confirmRemove = key(s))}>Remove</button>
					{/if}
				</li>
			{/each}
		</ul>
		<p class="muted small">Removing a source stops syncing it. Your files and cloud copies are not deleted.</p>
	{/if}
</section>

<style>
	.kinds { display: flex; gap: 16px; margin-bottom: 8px; }
	.row { display: flex; gap: 8px; align-items: center; }
	form .row input { flex: 1; min-width: 0; }
	ul { list-style: none; margin: 0; padding: 0; }
	li { display: flex; justify-content: space-between; gap: 12px; align-items: center; padding: 10px 0; border-top: 1px solid var(--line); }
	li:first-child { border-top: 0; }
	.info { min-width: 0; }
	.tag { font-weight: 600; }
	.bad { color: var(--bad); }
</style>
