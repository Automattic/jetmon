import { Container, getRandom } from "@cloudflare/containers";

export interface Env {
	VERIFLIER: DurableObjectNamespace<Veriflier>;
	VERIFLIER_AUTH_TOKEN: string;
}

export class Veriflier extends Container<Env> {
	defaultPort = 7803;
	sleepAfter = "10m";

	override envVars(): Record<string, string> {
		return {
			VERIFLIER_PORT: "7803",
			VERIFLIER_AUTH_TOKEN: this.env.VERIFLIER_AUTH_TOKEN,
		};
	}
}

export default {
	async fetch(req: Request, env: Env): Promise<Response> {
		const stub = await getRandom(env.VERIFLIER, 3);
		return stub.fetch(req);
	},
} satisfies ExportedHandler<Env>;
