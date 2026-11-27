// api.ts: the web view's connection to the WorldHop program. Every call to the local
// HTTP API (internal/api in the Go code) goes through here, so the pages never build
// URLs or parse errors themselves. The types describe the JSON the server sends; they
// are written by hand, so when a Go struct changes its json tags, update the matching
// type here. docs/technical.md section 2.4 lists the endpoints.

export type ProblemLevel = 'info' | 'warn' | 'error';

/** Something that kept a sync from finishing, in plain words. */
export type Problem = {
	kind: string;
	level: ProblemLevel;
	title: string;
	/** What to do about it. */
	action?: string;
	/** The page where the user can fix it, e.g. "/cloud". */
	fix?: string;
	/** The raw technical message, for a "Details" fold-out. */
	detail?: string;
	worlds?: string[] | null;
};

export type Progress = {
	world: string;
	action: 'checking' | 'uploading' | 'downloading';
	/** Worlds finished before this one. */
	done: number;
	total: number;
};

/**
 * What GET /api/status returns: whether a sync is running, the last result, what is
 * being worked on, and the problems that kept the last pass from finishing.
 */
export type Status = {
	state: 'idle' | 'syncing';
	lastSync: string | null;
	lastResult: string;
	/** '' until the first sync. */
	outcome: '' | 'ok' | 'waiting' | 'attention' | 'error';
	result: { total: number; uploaded: number; downloaded: number; upToDate: number };
	progress: Progress | null;
	problems: Problem[] | null;
	warnings: string[] | null;
	version: string;
};

/**
 * How a world compares with the cloud. Mirrors the Go sync.Status values; 'cloud-lost'
 * means the cloud no longer holds what this computer last saw, and 'unknown' means the
 * status could not be worked out (see the world's error).
 */
export type WorldStatus =
	| 'no-remote'
	| 'no-local'
	| 'synced'
	| 'local-ahead'
	| 'remote-ahead'
	| 'conflict'
	| 'cloud-lost'
	| 'unknown';

/**
 * One world on this computer, as listed on the Worlds tab.
 */
export type World = {
	id: string;
	name: string;
	path: string;
	launcher?: string;
	status: WorldStatus;
	open: boolean;
	/** The free version is already syncing a different world. */
	locked?: boolean;
	/** Why the status is unknown, in plain words. */
	error?: string;
	/** What to do about it, and the page where to do it. */
	hint?: string;
	fix?: string;
	/** What the cloud lost, when status is 'cloud-lost'. */
	lost?: Lost;
};

/** The cloud no longer holds what this computer last saw. */
export type Lost = {
	kind: 'versions' | 'world' | 'data';
	/** Versions this computer had seen that are gone, and how many are still listed. */
	versions: number;
	remaining: number;
	/** Packs found missing (0 when unknown). */
	packs: number;
	/** The cloud can be taken as it is now (not possible for missing data). */
	canAccept: boolean;
	/** This computer has a copy it can upload. */
	canReupload: boolean;
};

/**
 * What the user can choose to do about lost cloud data.
 */
export type LostAction = 'accept-cloud' | 'reupload';

/**
 * A world that is in the cloud but has no copy on this computer.
 */
export type CloudWorld = {
	id: string;
	name: string;
	updatedAt: string | null;
	size: number;
	files: number;
	/** Two computers uploaded without syncing; a conflict will be reported. */
	diverged: boolean;
	/** This computer had it before and it was removed, so it is not auto-downloaded. */
	seen: boolean;
	error?: string;
};

/**
 * A source is either a saves folder (every world inside) or a single world folder.
 */
export type SourceKind = 'saves' | 'world';

/**
 * A folder the user asked WorldHop to keep synced.
 */
export type Source = {
	kind: SourceKind;
	path: string;
	launcher?: string;
	/** Worlds this source currently resolves to. */
	worlds: number;
	error?: string;
};

/**
 * How to settle a conflict. Mirrors the Go sync.Strategy values.
 */
export type Strategy = 'keep-local' | 'keep-remote' | 'keep-newest' | 'keep-both';

/**
 * A local snapshot of a world, kept before WorldHop replaced it.
 */
export type Snapshot = {
	id: string;
	reason: string;
	createdAt: string | null;
};

/**
 * The license state shown on the License tab.
 */
export type License = {
	tier: 'free' | 'pro';
	freeWorlds: number;
	freeWorld?: string;
	/** Masked. */
	key?: string;
	verifiedAt?: string;
	/** Set when Pro is running on offline grace. */
	graceEnds?: string;
	canActivate: boolean;
	purchaseUrl?: string;
	error?: string;
};

/**
 * The cloud storage choices.
 */
export type ProviderId = 'gdrive' | 's3';

/**
 * A cloud storage choice as shown on the Cloud account tab.
 */
export type Provider = {
	id: ProviderId;
	name: string;
	/** False while this build has no implementation for the provider. */
	available: boolean;
	/** Why it is unavailable (missing OAuth client ID, -store mode). */
	note?: string;
	linked: boolean;
	/** Consent page opened; waiting for the user to finish in the browser. */
	pending: boolean;
	account?: string;
	/** e.g. access was revoked and the user needs to reconnect. */
	error?: string;
	/** Needs a Pro licence. */
	pro?: boolean;
	/** Pro, and this computer has no licence. */
	locked?: boolean;
};

/**
 * The saved S3-compatible storage settings. The secret key is never sent to the page.
 */
export type S3Config = {
	endpoint: string;
	region: string;
	bucket: string;
	prefix: string;
	pathStyle: boolean;
	accessKey: string;
	/** A secret key is stored in the system keychain. It is never sent to the page. */
	hasSecret: boolean;
};

/**
 * What the S3 form submits.
 */
export type S3Input = Omit<S3Config, 'hasSecret'> & {
	/** Leave empty to keep the stored secret. */
	secretKey: string;
};

/**
 * Sends a request to the local API and returns the JSON reply. A failed request throws
 * an Error whose message is the server's plain-language text (and what to do about it,
 * when it has one), so pages can show it as is.
 */
async function call<T>(method: string, url: string, body?: unknown): Promise<T> {
	const r = await fetch(url, {
		method,
		headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
		body: body === undefined ? undefined : JSON.stringify(body)
	});
	if (!r.ok) {
		let msg = `${method} ${url}: ${r.status}`;
		try {
			const j = await r.json();
			if (j?.error) msg = j.action ? `${j.error}. ${j.action}` : j.error;
		} catch {
			/* keep the default message */
		}
		throw new Error(msg);
	}
	if (r.status === 204) return undefined as T;
	return r.json();
}

/**
 * Current sync status.
 */
export const getStatus = () => call<Status>('GET', '/api/status');
/**
 * Runs a sync pass over every world now and returns the new status.
 */
export const forceSync = () => call<Status>('POST', '/api/sync');

/**
 * Lists the worlds on this computer with their status.
 */
export const getWorlds = () => call<World[]>('GET', '/api/worlds');
/**
 * Syncs a single world now.
 */
export const syncWorld = (id: string) =>
	call<Status>('POST', `/api/worlds/${encodeURIComponent(id)}/sync`);
/**
 * Carries out the user's choice about lost cloud data.
 */
export const resolveLost = (id: string, action: LostAction) =>
	call<{ action: LostAction; manifest?: string }>(
		'POST',
		`/api/worlds/${encodeURIComponent(id)}/lost`,
		{ action }
	);
/**
 * Settles a conflict with the chosen strategy.
 */
export const resolveConflict = (id: string, strategy: Strategy) =>
	call<{ used: Strategy; copy?: string }>(
		'POST',
		`/api/worlds/${encodeURIComponent(id)}/resolve`,
		{ strategy }
	);
/**
 * Lists a world's local snapshots, newest first.
 */
export const getSnapshots = (id: string) =>
	call<Snapshot[]>('GET', `/api/worlds/${encodeURIComponent(id)}/snapshots`);
/**
 * Swaps a snapshot back in as the world (the current one is snapshotted first).
 */
export const restoreSnapshot = (id: string, snapshot: string) =>
	call<void>('POST', `/api/worlds/${encodeURIComponent(id)}/restore`, { snapshot });

/**
 * Lists worlds that are in the cloud but not on this computer.
 */
export const getCloudWorlds = () => call<CloudWorld[]>('GET', '/api/cloud-worlds');
/**
 * Downloads a cloud-only world into a saves folder. It never overwrites an existing
 * folder.
 */
export const downloadCloudWorld = (id: string, saves: string) =>
	call<{ id: string }>('POST', `/api/cloud-worlds/${encodeURIComponent(id)}/download`, { saves });

/**
 * Lists the configured sources.
 */
export const getSources = () => call<Source[]>('GET', '/api/sources');
/**
 * Adds a source (a saves folder or a single world).
 */
export const addSource = (kind: SourceKind, path: string) =>
	call<Source[]>('POST', '/api/sources', { kind, path });
/** Opens the OS folder dialog on this computer. path is '' if cancelled. */
export const pickFolder = () => call<{ path: string }>('POST', '/api/pick-folder');
/**
 * Stops syncing a source. Nothing on disk or in the cloud is deleted.
 */
export const removeSource = (kind: SourceKind, path: string) =>
	call<Source[]>('POST', '/api/sources/remove', { kind, path });

/**
 * Lists the cloud storage choices and their state.
 */
export const getProviders = () => call<Provider[]>('GET', '/api/providers');
/**
 * Starts connecting a cloud account (opens the sign-in page for Google Drive).
 */
export const linkProvider = (id: ProviderId) =>
	call<{ url?: string }>('POST', `/api/providers/${id}/link`);
/**
 * Forgets the stored sign-in or keys. Cloud data is left alone.
 */
export const unlinkProvider = (id: ProviderId) =>
	call<void>('POST', `/api/providers/${id}/unlink`);

/**
 * The saved S3 settings.
 */
export const getS3 = () => call<S3Config>('GET', '/api/s3');
/**
 * Saves S3 settings and keys and makes S3 the storage in use. Pro only.
 */
export const saveS3 = (in_: S3Input) => call<S3Config>('PUT', '/api/s3', in_);
/**
 * Checks entered S3 settings against the bucket without saving them.
 */
export const testS3 = (in_: S3Input) =>
	call<{ ok: boolean; error?: string }>('POST', '/api/s3/test', in_);

// Tell the server this page is going away; it stops itself after a short grace
// period unless something else (e.g. a reload) talks to it first.
export function sayBye() {
	navigator.sendBeacon('/api/bye');
}

/**
 * The license state.
 */
export const getLicense = () => call<License>('GET', '/api/license');
/**
 * Verifies a license key with Gumroad and switches to Pro.
 */
export const activateLicense = (key: string) =>
	call<License>('POST', '/api/license/activate', { key });
/**
 * Forgets the license key on this computer (back to the free version).
 */
export const deactivateLicense = () => call<License>('POST', '/api/license/deactivate');
/**
 * Chooses which world the free version syncs.
 */
export const setFreeWorld = (id: string) => call<License>('POST', '/api/license/free-world', { id });

/**
 * The Encryption tab's state.
 */
export type Encryption = {
	/** Pro license active (needed to turn encryption on). */
	pro: boolean;
	enabled: boolean;
	/** The key is loaded, so syncing works. */
	unlocked: boolean;
	remember: boolean;
	/** The cloud store already has a key (set up on another computer); null = unknown. */
	keyInStore: boolean | null;
	/** Another computer changed the passphrase: enter the new one. */
	keyChanged?: boolean;
	/** The cloud's key header is older than the one this computer has used, or gone. */
	keyRollback?: boolean;
	/** A passphrase change running here, or its result until dismissed. */
	rekey?: Rekey;
	error?: string;
};

/**
 * A passphrase change in progress or just finished.
 */
export type Rekey = {
	state: 'running' | 'done' | 'failed' | 'cancelled';
	phase?: 'preparing' | 'copying' | 'finishing';
	/** Manifests and packs copied so far, and in all. */
	done: number;
	total: number;
	worlds: number;
	error?: string;
};

/**
 * The encryption state (also carries the progress of a passphrase change).
 */
export const getEncryption = () => call<Encryption>('GET', '/api/encryption');
/**
 * Turns encryption on: creates the key, or joins an existing one on another computer.
 */
export const enableEncryption = (b: {
	passphrase: string;
	confirm: string;
	acknowledged: boolean;
	remember: boolean;
}) => call<Encryption>('POST', '/api/encryption/enable', b);
/**
 * Enters the passphrase after a restart or a lock.
 */
export const unlockEncryption = (passphrase: string, remember: boolean) =>
	call<Encryption>('POST', '/api/encryption/unlock', { passphrase, remember });
/**
 * Forgets the key until the passphrase is entered again.
 */
export const lockEncryption = () => call<Encryption>('POST', '/api/encryption/lock');
/**
 * Turns encryption off on this computer. Encrypted data in the cloud stays encrypted.
 */
export const disableEncryption = () => call<Encryption>('POST', '/api/encryption/disable');
/**
 * Writes this computer's saved copy of the newest key information back to the cloud,
 * after the cloud lost it.
 */
export const restoreKeyHeader = () => call<Encryption>('POST', '/api/encryption/restore-key');
/**
 * Goes by whatever key information the cloud shows now, after it looked rolled back.
 */
export const acceptKeyRollback = () => call<Encryption>('POST', '/api/encryption/accept-key-rollback');
/**
 * Starts changing the passphrase: re-encrypts every world in the background.
 */
export const changePassphrase = (b: {
	current: string;
	passphrase: string;
	confirm: string;
	acknowledged: boolean;
	remember: boolean;
}) => call<Encryption>('POST', '/api/encryption/change-passphrase', b);
/**
 * Stops a passphrase change that is running. Nothing was changed.
 */
export const cancelChangePassphrase = () =>
	call<Encryption>('POST', '/api/encryption/change-passphrase/cancel');
/**
 * Clears the result of a finished passphrase change from the page.
 */
export const dismissChangePassphrase = () =>
	call<Encryption>('POST', '/api/encryption/change-passphrase/dismiss');

/**
 * Whether a newer WorldHop release exists, and where to get it.
 */
export type UpdateInfo = {
	available: boolean;
	current: string;
	latest?: string;
	/** Release page to open to download the new version. */
	url?: string;
};
/**
 * Checks whether a newer release is known.
 */
export const getUpdate = () => call<UpdateInfo>('GET', '/api/update');
