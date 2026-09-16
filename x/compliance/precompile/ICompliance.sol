// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

/// @title Konstellation compliance precompile
/// @notice Read-only view of the chain's compliance lists, at a fixed address
///         (COMPLIANCE_PRECOMPILE_ADDRESS). The chain itself refuses every
///         transaction that involves a frozen address; contracts that need to
///         refuse *internal* transfers to frozen addresses, or that want to
///         serve only verified addresses, call this.
/// @dev All functions are `view` and cost the precompile's base read gas.
interface ICompliance {
    /// @notice True if `account` is on the allow ("verified") list.
    function isVerified(address account) external view returns (bool);

    /// @notice True if `account` is on the block ("frozen") list and, for an
    ///         emergency freeze, the freeze has not yet expired.
    function isFrozen(address account) external view returns (bool);

    /// @notice Both flags, plus the emergency-freeze expiry as a unix time.
    ///         `frozenUntil` is 0 when not frozen or when the freeze is
    ///         permanent.
    function status(address account)
        external
        view
        returns (bool verified, bool frozen, uint64 frozenUntil);
}
