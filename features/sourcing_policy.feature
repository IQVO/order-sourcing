Feature: The versioned sourcing policy
  A cheaper cross-region lane always wins until an operator publishes a policy. The policy
  adds a surcharge to cross-region candidates and a cost-deviation budget inside which an
  in-region candidate beats a cheaper cross-region one (ADR 0014). Everything defaults to
  off, so nothing changes until a version is published.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"
    And the pickup lane of "fc-b" costs 1000 cents
    # fc-a is in the destination's region r1, fc-b is cross-region (r2).

  Scenario: Without a policy the cheaper cross-region lane wins
    Given the pickup lane of "fc-a" costs 1100 cents
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"
    When I release the assignment of "ord-1" group 1
    Then the last release event carries no policy version

  Scenario: Inside a 20 percent budget the in-region node beats a cheaper cross-region lane
    Given the pickup lane of "fc-a" costs 1200 cents
    And the operator publishes a sourcing policy with a cross-region surcharge of 0 cents and a budget of 20 percent
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 has the candidate node "fc-a"
    And the assignment of "ord-1" group 1 was queued under policy version 1
    When I release the assignment of "ord-1" group 1
    Then the last release event carries policy version 1 and no effective cost

  Scenario: One cent outside the budget the cheaper cross-region lane wins
    Given the pickup lane of "fc-a" costs 1201 cents
    And the operator publishes a sourcing policy with a cross-region surcharge of 0 cents and a budget of 20 percent
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"

  Scenario: The surcharge makes the cross-region lane dearer and shows on the release
    Given the pickup lane of "fc-a" costs 1100 cents
    And the operator publishes a sourcing policy with a cross-region surcharge of 300 cents and a budget of 0 percent
    And the node "fc-a" has no stock of "cat-1" any more
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    Then the last release event carries policy version 1 and effective cost 1300 cents

  Scenario: Re-optimising picks up a newly published policy
    Given the pickup lane of "fc-a" costs 1200 cents
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the operator publishes a sourcing policy with a cross-region surcharge of 0 cents and a budget of 20 percent
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-a"
    And the assignment of "ord-1" group 1 was queued under policy version 1

  Scenario: Every publish creates the next version and raises the analytics event
    Given the operator publishes a sourcing policy with a cross-region surcharge of 100 cents and a budget of 10 percent
    When the operator publishes a sourcing policy with a cross-region surcharge of 0 cents and a budget of 0 percent
    Then the last command published "SourcingPolicyPublished"
    And the current sourcing policy is version 2

  Scenario: A policy outside its ranges is refused and stores nothing
    When the operator publishes a sourcing policy with a cross-region surcharge of 100001 cents and a budget of 20 percent
    Then the command is rejected as invalid
    And the last command published nothing
    And the current sourcing policy is version 0
