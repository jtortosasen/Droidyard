import { describe, expect, expectTypeOf, it } from 'vitest';
import { eventTopics, type EventPayload, type EventType } from './capabilities.gen';

// The generated client types the events a capability's own stream publishes, from the
// schema's `events` (ADR-0041 §4): a schema that loses them, or renames a field the UI
// reads, fails `check` before it reaches a user.
describe('the generated event types', () => {
	it('name the topic and type the payloads of device.watchLogs', () => {
		expect(eventTopics['device.watchLogs']).toBe('device.logcat');
		expectTypeOf<EventType<'device.watchLogs'>>().toEqualTypeOf<'lines' | 'stopped'>();

		type Lines = EventPayload<'device.watchLogs', 'lines'>;
		expectTypeOf<Lines['dropped']>().toEqualTypeOf<number>();
		expectTypeOf<Lines['lines'][number]['message']>().toEqualTypeOf<string>();
		expectTypeOf<Lines['lines'][number]['priority']>().toEqualTypeOf<
			'verbose' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'
		>();

		type Stopped = EventPayload<'device.watchLogs', 'stopped'>;
		expectTypeOf<Stopped['reason']>().toEqualTypeOf<'deviceGone' | 'exited' | 'replaced'>();
		expectTypeOf<Stopped['cause']>().toEqualTypeOf<string | undefined>();
	});
});
