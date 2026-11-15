async function confirmClick(click) {
    try {
        const response = await fetch(endpoint, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ click }),
            signal: AbortSignal.timeout(16000),
        });
        return response.status === 204;
    } catch {
        return false;
    }
}

module.exports.default = async ({ page }) => {
    installClickGuard(page);
    const consoleMessage = installDiagnostics(page);
    const context = page.context();
    await protectFrame(await context.newCDPSession(page), consoleMessage);
    // Cross-origin frames have their own network and execution contexts.
    const protectedFrames = new WeakSet();
    page.on('framenavigated', frame => {
        if (frame !== page.mainFrame()) protectChild(frame).catch(() => {});
    });
    async function protectChild(frame) {
        if (protectedFrames.has(frame)) return;
        protectedFrames.add(frame);
        let session;
        try {
            session = await context.newCDPSession(frame);
            session.once('close', () => protectedFrames.delete(frame));
            await protectFrame(session, consoleMessage);
        } catch {
            protectedFrames.delete(frame);
            await session?.detach().catch(() => {});
        }
    }
};

async function protectFrame(session, consoleMessage) {
    const frames = new Map();
    const worlds = new Map();
    const closeFrame = frame => {
        frame.reopened = undefined;
        sealWorkerLogs(frame);
    };
    const rememberFrame = ({ id, url, urlFragment, loaderId }) => {
        const frame = frames.get(id);
        const address = url + (urlFragment || '');
        if (frame && frame.loaderId === loaderId) {
            frame.reopened = undefined;
            frame.url = address;
        } else {
            if (frame) closeFrame(frame);
            frames.set(id, { url: address, loaderId, locations: [], pendingLogs: new Set(), session });
        }
    };
    session.on('Page.frameNavigated', ({ frame }) => rememberFrame(frame));
    session.on('Page.documentOpened', ({ frame: { id } }) => {
        const frame = frames.get(id);
        if (frame && !frame.openDepth) {
            // An implicit or uninstrumented open has no timestamped boundary.
            // Drop unresolved output and wait for an instrumented open to recover.
            frame.untrackedOpen = true;
            if (frame.locations) frame.locations.length = 0;
            sealWorkerLogs(frame);
            // Chromium's frame URL can still describe the old document. Resolve
            // main-world logs only while this exact replacement remains current.
            frame.reopened = { frame, context: frame.world };
        }
    });
    session.on('Page.navigatedWithinDocument', ({ frameId, url }) => {
        const frame = frames.get(frameId);
        if (frame) {
            frame.reopened = undefined;
            frame.url = url;
        }
    });
    session.on('Page.frameDetached', ({ frameId }) => {
        const frame = frames.get(frameId);
        if (frame) closeFrame(frame);
        frames.delete(frameId);
    });
    session.once('close', () => {
        // This stream also carries the worker events. Once it closes there can
        // be no more document markers or worker events for these frames.
        for (const frame of frames.values()) closeFrame(frame);
        frames.clear();
    });
    session.on('Runtime.executionContextCreated', ({ context }) => {
        if (!context.auxData?.isDefault) return;
        const { frameId } = context.auxData;
        const frame = frames.get(frameId);
        if (frame) {
            frame.world = context;
            worlds.set(context.id, frame);
        }
    });
    session.on('Runtime.executionContextDestroyed', ({ executionContextId }) => {
        const frame = worlds.get(executionContextId);
        if (frame?.world?.id === executionContextId) frame.world = undefined;
        worlds.delete(executionContextId);
    });
    session.on('Runtime.executionContextsCleared', () => {
        for (const frame of worlds.values()) frame.world = undefined;
        worlds.clear();
    });
    function recordRuntime(method, params, frame, worker) {
        if (!frame) return;
        if (method === 'Runtime.consoleAPICalled') {
            const text = params.args.map(arg => arg.value ?? arg.unserializableValue ?? arg.description ?? arg.type).join(' ');
            if (text.startsWith(locationMarker)) {
                if (!worker) rememberLocation(frame, params.timestamp, text.slice(locationMarker.length));
            } else if (!text.startsWith(eventKey + ':')) {
                consoleMessage(frame, params.type, text, params.timestamp, worker);
            }
        } else if (method === 'Runtime.exceptionThrown') {
            const detail = params.exceptionDetails;
            consoleMessage(frame, 'error', detail.exception?.description ?? detail.text, params.timestamp, worker);
        }
    }
    session.on('Runtime.consoleAPICalled', params =>
        recordRuntime('Runtime.consoleAPICalled', params, worlds.get(params.executionContextId)));
    session.on('Runtime.exceptionThrown', params =>
        recordRuntime('Runtime.exceptionThrown', params, worlds.get(params.exceptionDetails.executionContextId)));

    const workerEvents = watchWorkerTargets(session.send.bind(session), frames, recordRuntime);
    for (const method of ['Target.attachedToTarget', 'Target.detachedFromTarget', 'Target.receivedMessageFromTarget']) {
        session.on(method, params => workerEvents(method, params));
    }
    session.on('Fetch.requestPaused', async ({ requestId, request, resourceType }) => {
        try {
            const { method, headers } = request;
            const hasMode = Object.keys(headers).some(key => key.toLowerCase() === 'sec-fetch-mode');
            if (resourceType !== 'Document' && !hasMode && ['GET', 'HEAD', 'OPTIONS'].includes(method)) {
                const forwarded = Object.entries(headers)
                    .filter(([key]) => key.toLowerCase() !== 'x-buddy-resource')
                    .map(([name, value]) => ({ name, value }));
                forwarded.push({ name: 'X-Buddy-Resource', value: resourceKey });
                await session.send('Fetch.continueRequest', { requestId, headers: forwarded });
            } else {
                await session.send('Fetch.continueRequest', { requestId });
            }
        } catch {
            await session.send('Fetch.failRequest', { requestId, errorReason: 'BlockedByClient' }).catch(() => {});
        }
    });
    await session.send('Page.enable');
    const { frameTree } = await session.send('Page.getFrameTree');
    function rememberTree(tree) {
        rememberFrame(tree.frame);
        for (const child of tree.childFrames ?? []) rememberTree(child);
    }
    rememberTree(frameTree);
    await session.send('Runtime.enable');
    // Non-flattened sessions deliver worker runtime events through this
    // CDP session alongside the browser's normal Playwright attachment.
    await session.send('Target.setAutoAttach', workerAutoAttach);
    await session.send('Fetch.enable', { patterns: [{ urlPattern: 'http://*', requestStage: 'Request' }] });
}

const workerAutoAttach = {
    autoAttach: true, waitForDebuggerOnStart: false, flatten: false,
    filter: [{ type: 'worker' }, { exclude: true }],
};

function watchWorkerTargets(send, frames, recordRuntime, parent) {
    const children = new Map();
    return (method, params) => {
        if (method === 'Target.attachedToTarget') {
            const { sessionId, targetInfo } = params;
            // Nested workers inherit the document, not the URL of their parent script.
            const frameId = parent?.frameId ?? targetInfo.parentFrameId;
            const owner = parent?.owner ?? frames.get(frameId);
            if (targetInfo.type !== 'worker' || !owner || !targetInfo.url) return;
            const worker = {
                frameId, owner, started: parent?.started ?? Date.now(),
                scripts: [...(parent?.scripts ?? []), targetInfo.url],
            };
            let command = 0;
            const sendChild = (method, params) => send('Target.sendMessageToTarget', {
                sessionId, message: JSON.stringify({ id: ++command, method, params }),
            });
            children.set(sessionId, watchWorkerTargets(sendChild, frames, recordRuntime, worker));
            sendChild('Target.setAutoAttach', workerAutoAttach).catch(() => {});
            sendChild('Runtime.enable').catch(() => children.delete(sessionId));
        } else if (method === 'Target.detachedFromTarget') {
            children.delete(params.sessionId);
        } else if (method === 'Target.receivedMessageFromTarget') {
            const child = children.get(params.sessionId);
            if (!child) return;
            const event = JSON.parse(params.message);
            if (event.method) child(event.method, event.params);
        } else if (parent && frames.get(parent.frameId) === parent.owner) {
            recordRuntime(method, params, parent.owner, parent);
        }
    };
}

function rememberLocation(frame, at, url) {
    if (url === 'opening') frame.reopened = undefined;
    if (!Number.isFinite(at) || !observeWorkerClock(frame, at)) return;
    // A newly observed location can have an earlier timestamp even when the
    // host has already caught up by the time Chrome delivers the event.
    if (frame.lastLocationAt > at) {
        invalidateWorkerLocations(frame);
        return;
    }
    frame.lastLocationAt = at;
    if (url === 'opening') {
        frame.openDepth = (frame.openDepth ?? 0) + 1;
        url = '';
    } else if (url.startsWith('opened ')) {
        frame.openDepth = Math.max(0, (frame.openDepth ?? 0) - 1);
        if (frame.openDepth) return;
        frame.untrackedOpen = false;
        url = url.slice('opened '.length);
    } else if (frame.openDepth || frame.untrackedOpen) {
        return;
    }
    // Only document-side markers carry a timestamp from the same clock as worker logs.
    const locations = frame.locations;
    if (url) frame.url = url;
    locations.push({ at, url });
    // A marker closes the preceding interval. Keep attribution with the log
    // before pruning history that is only needed for late-arriving events.
    sealWorkerLogs(frame, at);
    if (locations.length > 256) locations.shift();
}

function observeWorkerClock(frame, timestamp) {
    if (!frame.locations) return false;
    const now = Date.now();
    if (frame.clockAt > now || Math.trunc(timestamp) > now) {
        invalidateWorkerLocations(frame);
        return false;
    }
    frame.clockAt = now;
    return true;
}

function invalidateWorkerLocations(frame) {
    // Late worker events may have timestamps from either side of the reset.
    // Keep resolved logs and discard ambiguous output until another document.
    frame.locations = undefined;
    sealWorkerLogs(frame);
}

function locationAt(frame, timestamp) {
    if (!Number.isFinite(timestamp) || !frame.locations) return '';
    for (let i = frame.locations.length - 1; i >= 0; i--) {
        const { at, url } = frame.locations[i];
        if (at === timestamp) return '';
        if (at < timestamp) return url;
    }
    return '';
}

function sealWorkerLogs(frame, before = Infinity) {
    for (const record of frame.pendingLogs) {
        if (record.at >= before) continue;
        record.url = locationAt(frame, record.at);
        record.owner = undefined;
        frame.pendingLogs.delete(record);
    }
}

async function verifyReopenedPage(reopened) {
    const { frame, context } = reopened;
    if (!context?.uniqueId || frame.world !== context || frame.reopened !== reopened) return;
    try {
        const { result } = await frame.session.send('Runtime.evaluate', {
            uniqueContextId: context.uniqueId, expression: 'this.location.href', returnByValue: true,
        }, { timeout: 1000 });
        if (result.type === 'string' && frame.world === context && frame.reopened === reopened) {
            reopened.url = frame.url = result.value;
            frame.reopened = undefined;
        }
    } catch { /* Retry a detached or unresponsive replacement on a later diagnostic query. */ }
}

function installDiagnostics(page) {
    const consoleMessages = [];
    const requests = [];
    const requestRecords = new WeakMap();
    let navigation = 0;
    const remember = (log, item) => {
        log.push(item);
        return log.length > 256 ? log.shift() : undefined;
    };
    page.on('request', request => {
        if (request.isNavigationRequest()) {
            try {
                if (request.frame() === page.mainFrame()) navigation++;
            } catch { /* A popup can request its first page before attaching. */ }
        }
        const record = {
            at: Date.now(), navigation, url: request.url(), method: request.method(),
            kind: request.resourceType(),
        };
        requestRecords.set(request, record);
        remember(requests, record);
    });
    page.on('response', response => {
        const record = requestRecords.get(response.request());
        if (record) record.status = response.status();
    });
    page[Symbol.for('buddy.diagnostics')] = async ({ network, all, filter, includeStatic }) => {
        const match = filter ? new RegExp(filter) : null;
        const log = network ? requests : consoleMessages;
        const records = log.filter(record => (all || record.navigation === navigation) &&
            (!network || includeStatic || ['fetch', 'xhr'].includes(record.kind) || !record.status || record.status >= 400));
        if (network) return match ? records.filter(record => match.test(record.url)) : records;

        const cutoff = Date.now();
        const framesBySession = new Map();
        const reopenedPages = new Set();
        for (const { owner: frame, at, reopened } of records) {
            if (reopened && !reopened.url) reopenedPages.add(reopened);
            if (!frame || at >= cutoff) continue;
            if (!framesBySession.has(frame.session)) framesBySession.set(frame.session, new Set());
            framesBySession.get(frame.session).add(frame);
        }
        const workerChecks = Array.from(framesBySession, async ([session, frames]) => {
            try {
                // Drain earlier document-side events before attributing cross-target output.
                await session.send('Runtime.evaluate', { expression: 'void 0' }, { timeout: 1000 });
                for (const frame of frames) {
                    if (observeWorkerClock(frame)) sealWorkerLogs(frame, cutoff);
                }
            } catch { /* A detached or unresponsive document cannot attest to the URL. */ }
        });
        await Promise.all([...workerChecks, ...Array.from(reopenedPages, verifyReopenedPage)]);

        return records.flatMap(({ owner, reopened, ...record }) => {
            const url = reopened ? reopened.url : record.url;
            if (!url || (match && !match.test(url))) return [];
            return [{ ...record, url, at: Math.trunc(record.at) }];
        });
    };
    return (frame, kind, text, at, worker) => {
        if (worker && (frame.untrackedOpen || !Number.isFinite(at) || !observeWorkerClock(frame, at))) return;
        if (!worker && frame.openDepth) return;
        const record = {
            at, navigation, url: worker ? undefined : frame.url, kind, text: text.slice(0, 2048),
            owner: worker?.owner, workerScripts: worker?.scripts, workerStarted: worker?.started,
            reopened: worker ? undefined : frame.reopened,
        };
        if (worker) frame.pendingLogs.add(record);
        const evicted = remember(consoleMessages, record);
        evicted?.owner?.pendingLogs.delete(evicted);
    };
}

function installClickGuard(page) {
    let prepared;
    let executing;
    page.on('console', message => {
        if (executing && message.text() === eventKey + ':' + executing.token) executing.activated = true;
    });
    async function dispose(click) {
        if (!click) return;
        await click.gate?.evaluate(state => state.clear()).catch(() => {});
        await click.gate?.dispose().catch(() => {});
        await click.element?.dispose().catch(() => {});
    }
    page[Symbol.for('buddy.guardedClick')] = async ({ stage, token, target, options }) => {
        if (stage === 'prepare') {
            await dispose(prepared);
            prepared = { token };
            const locator = /^(f\d+)?e\d+$/.test(target) ? page.getByRef(target) : page.locator(target);
            prepared.element = await locator.elementHandle({ timeout: 3000 });
            prepared.gate = await prepared.element.evaluateHandle((node, { key, credential, token }) =>
                globalThis[key](credential, node, token), { key: eventKey, credential: secret, token });
            return true;
        }
        if (!prepared || prepared.token !== token) return false;
        const click = prepared;
        prepared = undefined;
        executing = click;
        try {
            if (!await click.gate.evaluate(state => state.probe())) {
                throw new Error('The browser click guard is missing from the current document.');
            }
            await click.element.click({ button: options?.button, timeout: 5000, trial: true });
            if (!await confirmClick(token) || !await click.gate.evaluate(state => state.arm())) {
                throw new Error('The browser click target or authorization changed before dispatch.');
            }
            await click.element.click({ ...options, timeout: 5000 });
            const [accepted, blocked] = await click.gate.evaluate(state => state.completed())
                .catch(() => [click.activated, false]);
            if (!accepted || blocked) {
                throw new Error('The browser click target changed during dispatch.');
            }
            return true;
        } finally {
            executing = undefined;
            await dispose(click);
        }
    };
}
