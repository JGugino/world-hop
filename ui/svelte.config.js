import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
	preprocess: vitePreprocess(),
	kit: {
		// Output is embedded into the Go binary (see ui/embed.go).
		adapter: adapter({ pages: 'build', assets: 'build', fallback: 'index.html' })
	}
};
