// Installed at document creation so the guard sees pointer events before site listeners.
((key, secret, locationMarker) => {
    const listen = EventTarget.prototype.addEventListener;
    const remove = EventTarget.prototype.removeEventListener;
    const dispatch = EventTarget.prototype.dispatchEvent;
    const NativeEvent = Event;
    const NativeCustomEvent = CustomEvent;
    const eventDetail = Object.getOwnPropertyDescriptor(CustomEvent.prototype, 'detail').get;
    const open = Object.getOwnPropertyDescriptor(Document.prototype, 'open');
    const defaultView = Object.getOwnPropertyDescriptor(Document.prototype, 'defaultView').get;
    const path = Event.prototype.composedPath;
    const includes = Array.prototype.includes;
    const prevent = Event.prototype.preventDefault;
    const stop = Event.prototype.stopImmediatePropagation;
    const closest = Element.prototype.closest;
    const outerHTML = Object.getOwnPropertyDescriptor(Element.prototype, 'outerHTML').get;
    const textContent = Object.getOwnPropertyDescriptor(Node.prototype, 'textContent').get;
    const connected = Object.getOwnPropertyDescriptor(Node.prototype, 'isConnected').get;
    const parent = Object.getOwnPropertyDescriptor(Node.prototype, 'parentElement').get;
    const call = Reflect.apply;
    const stringify = JSON.stringify;
    const report = console.debug.bind(console);
    const freeze = Object.freeze;
    const actions = ['click', 'dblclick', 'contextmenu', 'auxclick'];
    let armed;
    let guardPresent = false;
    let canRestoreClickListeners = true;
    const guardEvent = key + secret;
    const resetEvent = guardEvent + 'reset';
    const openEvent = guardEvent + 'open';
    const acknowledgeGuard = () => { guardPresent = true; };
    const navigation = window.navigation;
    const signal = navigation && Object.getOwnPropertyDescriptor(NavigateEvent.prototype, 'signal').get;
    const aborted = Object.getOwnPropertyDescriptor(AbortSignal.prototype, 'aborted').get;
    let pendingNavigation;
    function publishLocation(prefix = '') {
        pendingNavigation = undefined;
        report(locationMarker + prefix + location.href);
    }
    const noteLocation = event => { if (event.isTrusted) publishLocation(); };
    const noteTransition = event => {
        if (!event.isTrusted) return;
        pendingNavigation = call(signal, event, []);
        report(locationMarker);
    };
    const noteNavigationFailure = event => {
        if (event.isTrusted && pendingNavigation && call(aborted, pendingNavigation, [])) publishLocation();
    };

    function guard(event) {
        if (!armed) return;
        if (armed.probing) {
            call(prevent, event, []);
            call(stop, event, []);
            return;
        }
        if (!armed.blocked && call(includes, call(path, event, []), [armed.node]) && armed.matches()) {
            if (event.isTrusted && call(includes, actions, [event.type]) && !armed.accepted) {
                armed.accepted = true;
                report(key + ':' + armed.token);
            }
            return;
        }
        armed.blocked = true;
        call(prevent, event, []);
        call(stop, event, []);
    }

    function installClickListeners() {
        for (const type of ['pointerdown', 'mousedown', 'pointerup', 'mouseup', ...actions])
            call(listen, window, [type, guard, true]);
        call(listen, window, [guardEvent, acknowledgeGuard, true]);
    }

    function installLocationListeners() {
        call(listen, window, [openEvent, prepareOpen, true]);
        if (navigation) {
            call(listen, navigation, ['navigate', noteTransition, true]);
            call(listen, navigation, ['currententrychange', noteLocation, true]);
            call(listen, navigation, ['navigateerror', noteNavigationFailure, true]);
            publishLocation();
        }
    }

    function listenersReady() {
        guardPresent = false;
        call(dispatch, window, [new NativeEvent(guardEvent)]);
        return guardPresent;
    }

    function watchOpen(restoreClickListeners) {
        // A fresh listener distinguishes an actual reset from the no-op
        // overload path even if a previous implicit open removed our guard.
        let retained = false;
        const witness = () => { retained = true; };
        call(listen, window, [resetEvent, witness, true]);
        report(locationMarker + 'opening');
        return () => {
            try {
                retained = false;
                call(dispatch, window, [new NativeEvent(resetEvent)]);
                if (!retained) {
                    canRestoreClickListeners &&= restoreClickListeners;
                    if (canRestoreClickListeners) installClickListeners();
                    installLocationListeners();
                }
            } finally {
                call(remove, window, [resetEvent, witness, true]);
                publishLocation('opened ');
            }
        };
    }

    function prepareOpen(event) {
        // Site callbacks during a cross-frame reset can precede a restored
        // input guard. Only renew the location observer in this path.
        call(eventDetail, event, []).finish = watchOpen(false);
    }

    installClickListeners();
    installLocationListeners();
    Object.defineProperty(Document.prototype, 'open', { ...open, value: function (...args) {
        if (args.length >= 3) return call(open.value, this, args);
        let finish;
        if (this === document) {
            finish = watchOpen(true);
        } else {
            // Keep the native call in the caller's realm. The target frame
            // owns the markers and may outlive the caller during the reset.
            try {
                const target = call(defaultView, this, []);
                if (target) {
                    const handoff = { finish: undefined };
                    call(dispatch, target, [new NativeCustomEvent(openEvent, { detail: handoff })]);
                    finish = handoff.finish;
                }
            } catch { /* The native call handles an invalid or inaccessible receiver. */ }
        }
        try {
            return call(open.value, this, args);
        } finally {
            finish?.();
        }
    } });

    Object.defineProperty(window, key, { value: (credential, node, token) => {
        if (credential !== secret) throw new Error('Invalid browser click credential.');
        const url = location.href;
        const fingerprint = () => {
            const context = call(closest, node, ['tr, [role="row"], li, [role="listitem"], article, form']) ??
                call(parent, node, []);
            return stringify([call(outerHTML, node, []), context && call(textContent, context, [])]);
        };
        const expected = fingerprint();
        const matches = () => call(connected, node, []) && location.href === url && fingerprint() === expected;
        const state = { node, token, matches, accepted: false, blocked: false };
        return freeze({
            probe() {
                if (!listenersReady()) return false;
                state.probing = true;
                armed = state;
                return true;
            },
            arm() {
                if (!listenersReady() || !matches()) return false;
                state.probing = false;
                armed = state;
                return true;
            },
            completed: () => [state.accepted, state.blocked],
            clear() {
                if (armed === state) armed = undefined;
            },
        });
    } });
})(buddyEventKey, buddySecret, buddyLocationMarker);
