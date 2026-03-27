with open("tests/e2e/test_production_burst.py", "r") as f:
    code = f.read()

old_burst = "burst_tasks = [asyncio.create_task(burst_worker(i)) for i in range(BURST_SESSIONS)]"
new_burst = """burst_tasks = []
    for i in range(BURST_SESSIONS):
        burst_tasks.append(asyncio.create_task(burst_worker(i)))
        await asyncio.sleep(0.01)"""
        
code = code.replace(old_burst, new_burst)

with open("tests/e2e/test_production_burst.py", "w") as f:
    f.write(code)
