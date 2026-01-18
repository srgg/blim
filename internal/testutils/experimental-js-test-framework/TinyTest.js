var __tests = [];

function test(name, fn) {
    const err = new Error();
    let line = "<unknown>";
    if (err.stack) {
        const m = err.stack.split("\n")[1].match(/:(\d+):\d+/);
        if (m) line = m[1];
    }
    __tests.push({ name, fn, line });
}

// optional: helper to run all device_test manually
function runTests(t) {
    for (var i = 0; i < __tests.length; i++) {
        var tt = __tests[i];
        try {
            tt.fn();
            t.Log("PASS: " + tt.name);
        } catch (e) {
            t.Fail("FAIL: " + tt.name + " " + e);
        }
    }
}