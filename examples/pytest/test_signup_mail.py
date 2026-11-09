from app import send_verification_mail
from sandpost import extract


def test_verification_mail_carries_the_code(run_id):
    send_verification_mail("new.user@example.test", "482913", run_id)
    assert extract(run_id, query="subject:verify") == "482913"


def test_verification_mail_carries_the_link(run_id):
    send_verification_mail("link.user@example.test", "731904", run_id)
    assert extract(run_id, link="/verify") == "https://app.example.test/verify?code=731904"
