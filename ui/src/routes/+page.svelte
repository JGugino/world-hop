<script lang="ts">
	import { onMount } from 'svelte';
	import {
		getWorlds,
		syncWorld,
		resolveConflict,
		getSnapshots,
		restoreSnapshot,
		getCloudWorlds,
		getSources,
		downloadCloudWorld,
		type CloudWorld,
		type World,
		type WorldStatus,
		type Snapshot,
		type Strategy
	} from '$lib/api';
	import { status } from '$lib/status.svelte';

	let worlds = $state<World[] | null>(null);
	let error = $state('');
	let notice = $state('');
	let busy = $state<string | null>(null); // world id with an action running

	let openSnaps = $state<string | null>(null);
	let snaps = $state<Snapshot[]>([]);
	let confirmRestore = $state<string | null>(null);

	const labels: Record<WorldStatus, string> = {
		synced: 'Up to date',
		'local-ahead': 'Changes to upload',
		'remote-ahead': 'Newer version in the cloud',
		conflict: 'Conflict',
		'no-remote': 'Not uploaded yet',
		'no-local': 'Only in the cloud',
		unknown: 'Unknown'
	};
	const tone: Record<WorldStatus, string> = {
		synced: 'ok',
		'local-ahead': 'info',
		'remote-ahead': 'info',
		conflict: 'bad',
		'no-remote': 'info',
		'no-local': 'info',
		unknown: 'muted'
	};

	const choices: { s: Strategy; label: string; help: string }[] = [
		{ s: 'keep-local', label: 'Keep local', help: 'Use this computer’s version and upload it.' },
		{ s: 'keep-remote', label: 'Keep remote', help: 'Use the cloud version. A snapshot of this one is saved first.' },
		{ s: 'keep-newest', label: 'Keep newest', help: 'Use whichever was changed most recently.' },
		{ s: 'keep-both', label: 'Keep both', help: 'Copy this version to a new world folder, then use the cloud version.' }
	];

	let cloud = $state<CloudWorld[]>([]);
	let cloudError = $state('');
	let saves = $state<string[]>([]); // saves folders a world can be downloaded into
	let target = $state(''); // chosen folder

	async function load() {
		try {
			worlds = await getWorlds();
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		try {
			cloud = await getCloudWorlds();
			cloudError = '';
		} catch (e) {
			cloud = [];
			cloudError = e instanceof Error ? e.message : String(e);
		}
		try {
			saves = (await getSources()).filter((s) => s.kind === 'saves').map((s) => s.path);
			if (!saves.includes(target)) target = saves[0] ?? '';
		} catch {
			/* the Saves tab reports this */
		}
	}

	function size(n: number) {
		if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
		if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} MB`;
		return `${(n / 1024 ** 3).toFixed(2)} GB`;
	}

	const download = (c: CloudWorld) =>
		run(c.id, async () => {
			await downloadCloudWorld(c.id, target);
			notice = `${c.name}: downloaded to ${target}.`;
		});

	async function run(id: string, fn: () => Promise<unknown>) {
		busy = id;
		error = notice = '';
		try {
			await fn();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		busy = null;
		await load();
		status.refresh();
	}

	const sync = (w: World) => run(w.id, () => syncWorld(w.id));

	const resolve = (w: World, s: Strategy) =>
		run(w.id, async () => {
			const r = await resolveConflict(w.id, s);
			notice = `${w.name}: resolved with “${r.used}”.` + (r.copy ? ` Your version was kept at ${r.copy}.` : '');
		});

	async function toggleSnaps(w: World) {
		confirmRestore = null;
		if (openSnaps === w.id) {
			openSnaps = null;
			return;
		}
		try {
			snaps = await getSnapshots(w.id);
			openSnaps = w.id;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	const restore = (w: World, s: Snapshot) =>
		run(w.id, async () => {
			await restoreSnapshot(w.id, s.id);
			confirmRestore = null;
			notice = `${w.name}: restored. The next sync uploads it as a new version.`;
			snaps = await getSnapshots(w.id);
		});

	function when(iso: string | null) {
		return iso ? new Date(iso).toLocaleString() : '';
	}

	onMount(() => {
		load();
		const t = setInterval(() => {
			if (!busy) load();
		}, 10000);
		return () => clearInterval(t);
	});

	// Reload right after a sync pass finishes.
	$effect(() => {
		status.generation;
		load();
	});
</script>

{#if error}<p class="msg bad">{error}</p>{/if}
{#if notice}<p class="msg ok">{notice}</p>{/if}

{#if worlds === null}
	<p class="muted">Loading…</p>
{:else if worlds.length === 0 && cloud.length === 0}
	<div class="card">
		<h2>No worlds yet</h2>
		<p class="muted">Add a saves folder or a single world on the Saves tab.</p>
		<a href="/sources">Go to Saves</a>
	</div>
{:else}
	{#if cloud.length > 0}
		<section class="card">
			<h2>Cloud worlds</h2>
			<p class="muted small">
				These worlds are in your cloud storage but not on this computer. Downloading creates a new
				folder and never overwrites anything.
			</p>
			{#if saves.length === 0}
				<p class="note">
					Add a saves folder on the <a href="/sources">Saves</a> tab to choose where to put them.
				</p>
			{:else if saves.length > 1}
				<label class="small">
					Download into
					<select bind:value={target}>
						{#each saves as p}<option value={p}>{p}</option>{/each}
					</select>
				</label>
			{/if}
			<ul class="cloud">
				{#each cloud as c (c.id)}
					<li>
						<div>
							<b>{c.name}</b>
							<span class="muted small">
								· {size(c.size)} · {c.files} files{#if c.updatedAt} · updated {when(c.updatedAt)}{/if}
							</span>
							{#if c.diverged}
								<p class="note">
									Two computers changed this world separately. You get the newest version and are
									asked how to settle it on the next sync.
								</p>
							{/if}
							{#if c.error}<p class="note bad">{c.error}</p>{/if}
						</div>
						<button disabled={busy === c.id || !target || !!c.error} onclick={() => download(c)}>
							{busy === c.id ? 'Downloading…' : 'Download'}
						</button>
					</li>
				{/each}
			</ul>
		</section>
	{:else if cloudError}
		<p class="muted small">Cloud worlds unavailable: {cloudError}</p>
	{/if}
	{#each worlds as w (w.id)}
		<section class="card world">
			<div class="head">
				<div class="who">
					<span class="ico" aria-hidden="true"></span>
					<div>
						<h3>{w.name}</h3>
						<p class="muted small"><code>{w.path}</code></p>
					</div>
				</div>
				<span class="badge {tone[w.status]}">{labels[w.status]}</span>
			</div>

			{#if w.locked}
				<p class="note">
					The free version syncs 1 world, and it is set to a different one.
					<a href="/license">Upgrade to Pro or change the free world</a>.
				</p>
			{/if}
			{#if w.open}
				<p class="note">Open in Minecraft. It is synced after you close it.</p>
			{/if}
			{#if w.error}
				<p class="note bad">
					{w.error}.
					{#if w.hint}{w.hint}{/if}
					{#if w.fix && w.fix !== '/'}<a href={w.fix}>Fix it</a>{/if}
				</p>
			{/if}

			{#if w.status === 'conflict'}
				<div class="conflict">
					<p>
						This world changed on this computer and somewhere else. Nothing has been
						overwritten. Choose what to keep:
					</p>
					<div class="row">
						{#each choices as c}
							<button
								class={c.s === 'keep-both' ? '' : 'secondary'}
								title={c.help}
								disabled={busy === w.id || w.open}
								onclick={() => resolve(w, c.s)}>{c.label}</button>
						{/each}
					</div>
					<ul class="muted small">
						{#each choices as c}<li><b>{c.label}:</b> {c.help}</li>{/each}
					</ul>
				</div>
			{/if}

			<div class="row">
				<button class="secondary" disabled={busy === w.id || w.open || w.locked} onclick={() => sync(w)}>
					{busy === w.id ? 'Working…' : 'Sync this world'}
				</button>
				<button class="secondary" onclick={() => toggleSnaps(w)}>
					{openSnaps === w.id ? 'Hide snapshots' : 'Snapshots'}
				</button>
			</div>

			{#if openSnaps === w.id}
				<div class="snaps">
					{#if snaps.length === 0}
						<p class="muted small">
							No snapshots yet. One is saved automatically before any sync overwrites this world.
						</p>
					{:else}
						<ul>
							{#each snaps as s (s.id)}
								<li>
									<span>
										{when(s.createdAt) || s.id}
										{#if s.reason}<span class="muted small"> · {s.reason}</span>{/if}
									</span>
									{#if confirmRestore === s.id}
										<span class="row">
											<button class="danger" disabled={w.open || busy === w.id} onclick={() => restore(w, s)}>
												Replace current world
											</button>
											<button class="secondary" onclick={() => (confirmRestore = null)}>Cancel</button>
										</span>
									{:else}
										<button class="secondary" disabled={w.open} onclick={() => (confirmRestore = s.id)}>
											Restore
										</button>
									{/if}
								</li>
							{/each}
						</ul>
					{/if}
				</div>
			{/if}
		</section>
	{/each}
{/if}

<style>
	.world h3 { margin-bottom: 4px; }
	.ico { flex: none; width: 36px; height: 36px; border: 2px solid #000; background: var(--grass); background-size: 72px 72px; position: relative; }
	.ico::after { content: ''; position: absolute; left: 0; right: 0; top: 10px; bottom: 0; background: var(--dirt); background-size: 72px 72px; }
	.who { display: flex; gap: 14px; align-items: flex-start; }
	.head { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; }
	.row { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-top: 10px; }
	.badge { font-size: 0.8rem; padding: 2px 9px; white-space: nowrap; background: #111315; border: 2px solid currentColor; align-self: flex-start; }
	.badge.ok { color: var(--ok); }
	.badge.info { color: var(--info); }
	.badge.bad { color: var(--bad); }
	.badge.muted { color: var(--muted); }
	.note { margin: 8px 0 0; font-size: 0.92rem; color: var(--warn); }
	.note.bad { color: var(--bad); }
	.conflict { border: 2px solid #000; border-left: 6px solid var(--bad); background: #00000040; padding: 4px 14px 10px; margin-top: 12px; }
	.conflict ul { padding-left: 18px; margin: 8px 0 0; }
	.snaps { margin-top: 12px; border: 2px solid #000; background: #111315; box-shadow: inset 2px 2px 0 #000; padding: 4px 12px; }
	.snaps ul { list-style: none; margin: 0; padding: 0; }
	.snaps li { display: flex; justify-content: space-between; align-items: center; gap: 8px; padding: 6px 0; flex-wrap: wrap; }
	.cloud { list-style: none; margin: 8px 0 0; padding: 0; }
	.cloud li { display: flex; justify-content: space-between; align-items: center; gap: 8px; padding: 10px 0; border-top: 2px solid #0006; flex-wrap: wrap; }
	label.small { display: block; margin-top: 8px; }
	select { max-width: 100%; }
	.msg { margin: 14px 0 0; padding: 8px 12px; background: var(--panel); border: 2px solid #000; box-shadow: inset 2px 2px 0 var(--panel-hi), inset -2px -2px 0 var(--panel-lo); font-size: 0.92rem; }
	.msg.bad { border-left: 3px solid var(--bad); }
	.msg.ok { border-left: 3px solid var(--ok); }
</style>
