// The official transport checks message shape and the sending window. This
// example also binds that window to one origin before delivering private data.
import { PostMessageTransport } from "@modelcontextprotocol/ext-apps/app-bridge";
import type { JSONRPCMessage, TransportSendOptions } from "@modelcontextprotocol/client";

export class OriginTransport extends PostMessageTransport {
  constructor(private readonly peer: Window, private readonly origin: string) {
    super(peer, peer);
  }

  private readonly rejectOtherOrigin = (event: MessageEvent<unknown>): void => {
    if (event.source === this.peer && event.origin !== this.origin) {
      event.stopImmediatePropagation();
    }
  };

  override async start(): Promise<void> {
    window.addEventListener("message", this.rejectOtherOrigin, true);
    try {
      await super.start();
    } catch (error) {
      window.removeEventListener("message", this.rejectOtherOrigin, true);
      throw error;
    }
  }

  override async send(message: JSONRPCMessage, _options?: TransportSendOptions): Promise<void> {
    this.peer.postMessage(message, this.origin);
  }

  override async close(): Promise<void> {
    window.removeEventListener("message", this.rejectOtherOrigin, true);
    await super.close();
  }
}
