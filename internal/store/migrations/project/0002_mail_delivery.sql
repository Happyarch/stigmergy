-- Mail delivery.
--
-- read_at says the recipient opened the message. notified_at says stigmergy put
-- it in front of them. They are not the same thing, and conflating them is what
-- made the mailbox pull-only: an unread message was indistinguishable from an
-- undelivered one, so nothing could tell whether an agent had ignored its mail
-- or had simply never been told it had any.
--
-- With the two separated, the Stop hook can interrupt exactly once per message:
-- it blocks on messages that have never been announced, stamps them, and lets
-- the agent go. An agent that reads its mail and decides to press on is not
-- trapped in a loop, and a message that arrives mid-turn still gets its one
-- interruption.
ALTER TABLE mailbox_messages ADD COLUMN notified_at TEXT;

CREATE INDEX idx_msgs_unnotified ON mailbox_messages(to_root)
  WHERE notified_at IS NULL AND read_at IS NULL;
