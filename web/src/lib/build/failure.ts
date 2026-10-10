// How a failed call is shown: the code (the only thing to branch on), the daemon's
// message and its next step, all as text. A lost connection has no code.
import { CapabilityError } from '../api/client';

export interface Failure {
	code?: string;
	message: string;
	nextStep?: string;
}

export function failure(e: unknown): Failure {
	if (e instanceof CapabilityError) return { code: e.code, message: e.message, nextStep: e.nextStep };
	if (e instanceof Error) return { message: e.message };
	return { message: 'the call failed' };
}

/** `CODE: message` and the next step, as one line of text. */
export function failureText(f: Failure): string {
	const head = f.code ? `${f.code}: ${f.message}` : f.message;
	return f.nextStep ? `${head} Next step: ${f.nextStep}` : head;
}

/** The short form for a toolbar: `Failed: CODE`, or `Failed` for a failure without a code. */
export function failureShort(f: Failure): string {
	return f.code ? `Failed: ${f.code}` : 'Failed';
}
