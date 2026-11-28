<!--
The Settings tab. For now one option: how many snapshots (versions) of each world to
keep in the cloud. Every upload is a version you can restore from the Worlds tab; this
sets how many of the newest ones cloud cleanup keeps. Saving applies at the next
cleanup (at most once a day).
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { getSettings, saveSettings, type Settings } from '$lib/api';

	let settings = $state<Settings | null>(null);
	let value = $state(4);
	let error = $state('');
	let saved = $state(false);
	let busy = $state(false);

	// load reads the current setting and its allowed range.
	async function load() {
		try {
			settings = await getSettings();
			value = settings.cloudSnapshots;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	// save stores the number; the server refuses values outside the range.
	async function save() {
		busy = true;
		error = '';
		saved = false;
		try {
			settings = await saveSettings(Number(value));
			value = settings.cloudSnapshots;
			saved = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		busy = false;
	}

	onMount(load);
</script>

{#if !settings}
	{#if error}<p class="bad">{error}</p>{:else}<p class="muted">Loading…</p>{/if}
{:else}
	<section class="card">
		<h2>Snapshots kept in the cloud</h2>
		<p class="muted">
			Every upload is saved in your cloud storage as a version you can go back to from the Worlds
			tab (Snapshots → In the cloud). Choose how many of the newest versions of each world to keep.
			Older ones are removed to free space, but anything uploaded in the last 14 days is always kept.
		</p>
		<form
			class="row"
			onsubmit={(e) => {
				e.preventDefault();
				save();
			}}
		>
			<label for="keep">Versions per world</label>
			<input
				id="keep"
				type="number"
				min={settings.minCloudSnapshots}
				max={settings.maxCloudSnapshots}
				bind:value
				style="width: 6em"
			/>
			<button disabled={busy || Number(value) === settings.cloudSnapshots}>Save</button>
		</form>
		<p class="muted small">
			At least {settings.minCloudSnapshots}, at most {settings.maxCloudSnapshots}. Takes effect at the next
			cleanup, which runs about once a day after a good sync.
		</p>
		{#if error}<p class="bad">{error}</p>{/if}
		{#if saved}<p class="ok">Saved.</p>{/if}
	</section>
{/if}

<style>
	.row { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; margin-top: 10px; }
</style>
