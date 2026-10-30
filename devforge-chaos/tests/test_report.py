from report import Record, append_record, load_records, percentile, render_markdown, write_report


def test_percentile_nearest_rank():
    assert percentile([1, 2, 3, 4, 5, 6, 7, 8, 9, 10], 95) == 10
    assert percentile([5], 95) == 5
    assert percentile([4, 1, 3, 2], 50) == 2


def test_empty_report():
    assert "No experiments have run yet" in render_markdown([])


def test_report_stats_and_timeline(tmp_path):
    d = str(tmp_path)
    append_record(d, Record("pod-kill", "default", ["a"], "success", 4.0))
    append_record(d, Record("pod-kill", "default", ["b"], "success", 8.0))
    append_record(d, Record("node-drain", "default", ["c"], "not-recovered", None, "timed out"))
    assert len(load_records(d)) == 3

    path = write_report(d)
    text = open(path, encoding="utf-8").read()
    assert "| pod-kill | 2 | 2 | 6.0 | 8.0 | 8.0 | 0 |" in text
    assert "| node-drain | 1 | 0 | - | - | - | 1 |" in text
    assert "timed out" in text
