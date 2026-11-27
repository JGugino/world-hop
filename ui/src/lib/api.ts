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

export type WorldStatus =
	| 'no-remote'
	| 'no-local'
	| 'synced'
	| 'local-ahead'
	| 'remote-ahead'
	| 'conflict'
	| 'unknown';

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
};

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

export type SourceKind = 'saves' | 'world';

export type Source = {
	kind: SourceKind;
	path: string;
	launcher?: string;
	/** Worlds this source currently resolves to. */
	worlds: number;
	error?: string;
};

export type Strategy = 'keep-local' | 'keep-remote' | 'keep-newest' | 'keep-both';

export type Snapshot = {
	id: string;
	reason: string;
	createdAt: string | null;
};

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

export type ProviderId = 'gdrive' | 's3';

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

export type S3Input = Omit<S3Config, 'hasSecret'> & {
	/** Leave empty to keep the stored secret. */
	secretKey: string;
};

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

export const getStatus = () => call<Status>('GET', '/api/status');
export const forceSync = () => call<Status>('POST', '/api/sync');

export const getWorlds = () => call<World[]>('GET', '/api/worlds');
export const syncWorld = (id: string) =>
	call<Status>('POST', `/api/worlds/${encodeURIComponent(id)}/sync`);
export const resolveConflict = (id: string, strategy: Strategy) =>
	call<{ used: Strategy; copy?: string }>(
		'POST',
		`/api/worlds/${encodeURIComponent(id)}/resolve`,
		{ strategy }
	);
export const getSnapshots = (id: string) =>
	call<Snapshot[]>('GET', `/api/worlds/${encodeURIComponent(id)}/snapshots`);
export const restoreSnapshot = (id: string, snapshot: string) =>
	call<void>('POST', `/api/worlds/${encodeURIComponent(id)}/restore`, { snapshot });

export const getCloudWorlds = () => call<CloudWorld[]>('GET', '/api/cloud-worlds');
export const downloadCloudWorld = (id: string, saves: string) =>
	call<{ id: string }>('POST', `/api/cloud-worlds/${encodeURIComponent(id)}/download`, { saves });

export const getSources = () => call<Source[]>('GET', '/api/sources');
export const addSource = (kind: SourceKind, path: string) =>
	call<Source[]>('POST', '/api/sources', { kind, path });
/** Opens the OS folder dialog on this computer. path is '' if cancelled. */
export const pickFolder = () => call<{ path: string }>('POST', '/api/pick-folder');
export const removeSource = (kind: SourceKind, path: string) =>
	call<Source[]>('POST', '/api/sources/remove', { kind, path });

export const getProviders = () => call<Provider[]>('GET', '/api/providers');
export const linkProvider = (id: ProviderId) =>
	call<{ url?: string }>('POST', `/api/providers/${id}/link`);
export const unlinkProvider = (id: ProviderId) =>
	call<void>('POST', `/api/providers/${id}/unlink`);

export const getS3 = () => call<S3Config>('GET', '/api/s3');
export const saveS3 = (in_: S3Input) => call<S3Config>('PUT', '/api/s3', in_);
export const testS3 = (in_: S3Input) =>
	call<{ ok: boolean; error?: string }>('POST', '/api/s3/test', in_);

// Tell the server this page is going away; it stops itself after a short grace
// period unless something else (e.g. a reload) talks to it first.
export function sayBye() {
	navigator.sendBeacon('/api/bye');
}

export const getLicense = () => call<License>('GET', '/api/license');
export const activateLicense = (key: string) =>
	call<License>('POST', '/api/license/activate', { key });
export const deactivateLicense = () => call<License>('POST', '/api/license/deactivate');
export const setFreeWorld = (id: string) => call<License>('POST', '/api/license/free-world', { id });

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
	/** A passphrase change running here, or its result until dismissed. */
	rekey?: Rekey;
	error?: string;
};

export type Rekey = {
	state: 'running' | 'done' | 'failed' | 'cancelled';
	phase?: 'preparing' | 'copying' | 'finishing';
	/** Manifests and packs copied so far, and in all. */
	done: number;
	total: number;
	worlds: number;
	error?: string;
};

export const getEncryption = () => call<Encryption>('GET', '/api/encryption');
export const enableEncryption = (b: {
	passphrase: string;
	confirm: string;
	acknowledged: boolean;
	remember: boolean;
}) => call<Encryption>('POST', '/api/encryption/enable', b);
export const unlockEncryption = (passphrase: string, remember: boolean) =>
	call<Encryption>('POST', '/api/encryption/unlock', { passphrase, remember });
export const lockEncryption = () => call<Encryption>('POST', '/api/encryption/lock');
export const disableEncryption = () => call<Encryption>('POST', '/api/encryption/disable');
export const changePassphrase = (b: {
	current: string;
	passphrase: string;
	confirm: string;
	acknowledged: boolean;
	remember: boolean;
}) => call<Encryption>('POST', '/api/encryption/change-passphrase', b);
export const cancelChangePassphrase = () =>
	call<Encryption>('POST', '/api/encryption/change-passphrase/cancel');
export const dismissChangePassphrase = () =>
	call<Encryption>('POST', '/api/encryption/change-passphrase/dismiss');

export type UpdateInfo = {
	available: boolean;
	current: string;
	latest?: string;
	/** Release page to open to download the new version. */
	url?: string;
};
export const getUpdate = () => call<UpdateInfo>('GET', '/api/update');
