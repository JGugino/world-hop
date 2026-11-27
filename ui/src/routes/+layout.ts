// +layout.ts: the app is a static single-page site (no server rendering or prerendering), because the Go program embeds and serves the built files.
export const ssr = false;
export const prerender = false;
