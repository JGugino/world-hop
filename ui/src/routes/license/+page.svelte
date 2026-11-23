<script lang="ts">
	import { onMount } from 'svelte';
	import {
		getLicense,
		activateLicense,
		deactivateLicense,
		setFreeWorld,
		getWorlds,
		type License,
		type World
	} from '$lib/api';

	let lic = $state<License | null>(null);
	let worlds = $state<World[]>([]);
	let key = $state('');
	let error = $state('');
	let busy = $state(false);
	let confirmRemove = $state(false);

	const msg = (e: unknown) => {
		const m = e instanceof Error ? e.message : String(e);
		return m.charAt(0).toUpperCase() + m.slice(1);
	};

	async function load() {
		try {
			[lic, worlds] = await Promise.all([getLicense(), getWorlds()]);
		} catch (e) {
			error = msg(e);
		}
	}

	async function run(fn: () => Promise<License>) {
		busy = true;
		error = '';
		try {
			lic = await fn();
			worlds = await getWorlds();
		} catch (e) {
			error = msg(e);
		}
		busy = false;
	}

	const activate = () =>
		run(async () => {
			const l = await activateLicense(key);
			key = '';
			return l;
		});

	const remove = () =>
		run(async () => {
			confirmRemove = false;
			return deactivateLicense();
		});

	const when = (iso?: string) => (iso ? new Date(iso).toLocaleDateString() : '');

	onMount(load);
</script>

{#if !lic}
	{#if error}<p class="bad">{error}</p>{:else}<p class="muted">Loading…</p>{/if}
{:else if lic.tier === 'pro'}
	{#if error}<p class="bad">{error}</p>{/if}
	<section class="card">
		<h2>WorldHop Pro</h2>
		<p class="ok">Active. Every world can sync.</p>
		{#if lic.key}<p class="muted small">License key <code>{lic.key}</code></p>{/if}
		{#if lic.graceEnds}
			<p class="warn small">
				WorldHop couldn’t check your license recently. Pro keeps working until {when(lic.graceEnds)}
				and re-checks whenever you’re online.
			</p>
		{/if}
		<div class="row">
			{#if confirmRemove}
				<button class="danger" disabled={busy} onclick={remove}>Remove key from this computer</button>
				<button class="secondary" onclick={() => (confirmRemove = false)}>Cancel</button>
			{:else}
				<button class="secondary" onclick={() => (confirmRemove = true)}>Remove key</button>
			{/if}
		</div>
		{#if confirmRemove}
			<p class="muted small">
				This computer goes back to the free version (1 world). Your key still works on your other
				computers and if you enter it again.
			</p>
		{/if}
	</section>
{:else}
	<section class="card">
		<h2>Free version</h2>
		<p class="muted">
			The free version syncs {lic.freeWorlds} world on this computer. Pro syncs as many worlds as you like.
		</p>
		{#if lic.error}<p class="warn small">{lic.error}</p>{/if}
		{#if worlds.length > 0}
			<label class="field">
				<span>World the free version syncs</span>
				<select
					value={lic.freeWorld ?? ''}
					disabled={busy}
					onchange={(e) => run(() => setFreeWorld(e.currentTarget.value))}>
					{#if !lic.freeWorld}<option value="" disabled>First world to sync is picked</option>{/if}
					{#each worlds as w (w.id)}<option value={w.id}>{w.name}</option>{/each}
				</select>
			</label>
		{/if}
	</section>

	<section class="card">
		<h2>Upgrade to Pro</h2>
		{#if !lic.canActivate}
			<p class="warn small">
				This build of WorldHop doesn’t have Pro licensing set up (no Gumroad product ID was compiled
				in), so a key can’t be checked here. Official releases include it. If you built WorldHop
				yourself, rebuild with <code>GUMROAD_PRODUCT_ID</code> set.
			</p>
		{/if}
		{#if lic.purchaseUrl}
			<p><a href={lic.purchaseUrl} target="_blank" rel="noopener">Get a license key</a>, then paste it here.</p>
		{:else}
			<p class="muted">Paste the license key from your purchase receipt.</p>
		{/if}
		<form
			class="row"
			onsubmit={(e) => {
				e.preventDefault();
				activate();
			}}>
			<input
				type="text"
				bind:value={key}
				placeholder="License key"
				aria-label="License key"
				autocomplete="off"
				spellcheck="false" />
			<button type="submit" disabled={busy || !key.trim()}>{busy ? 'Checking…' : 'Activate'}</button>
		</form>
		{#if error}<p class="bad small" role="alert">{error}</p>{/if}
		<p class="muted small">Activating needs an internet connection. After that, Pro keeps working offline for a while.</p>
	</section>
{/if}

<style>
	.row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
	.row input { flex: 1; min-width: 220px; }
	.ok { color: var(--ok); }
	.bad { color: var(--bad); }
	.warn { color: var(--warn); }
	.field { display: flex; flex-direction: column; gap: 4px; margin-top: 8px; font-size: 0.92rem; }
</style>
